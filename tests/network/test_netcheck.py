"""Tests for scripts/netcheck (public-state drift checker).

Run with either:
    python3 -m unittest discover -s tests/network -v
    python3 -m pytest tests/network

No network access: `netcheck.fetch` is monkeypatched with canned answers that
mirror the real API shapes observed on 2026-09-10.
"""

from __future__ import annotations

import copy
import importlib.machinery
import importlib.util
import io
import json
import os
import sys
import unittest
from contextlib import redirect_stdout

import yaml

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), os.pardir, os.pardir))
NETCHECK = os.path.join(REPO, "scripts", "netcheck")
PLAN = os.path.join(REPO, "infra", "network", "address-plan.yaml")
ROAS = os.path.join(REPO, "infra", "network", "rpki", "desired-roas.yaml")


def load_module():
    loader = importlib.machinery.SourceFileLoader("netcheck", NETCHECK)
    spec = importlib.util.spec_from_loader("netcheck", loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


nc = load_module()

with open(PLAN, encoding="utf-8") as fh:
    PLAN_DATA = yaml.safe_load(fh)
with open(ROAS, encoding="utf-8") as fh:
    ROAS_DATA = yaml.safe_load(fh)

V6 = "2a0f:85c1:368::/48"
V4 = "44.32.58.0/24"
ZONE = "as215520.net"
CF_NS = ["fay.ns.cloudflare.com.", "kip.ns.cloudflare.com."]


# ---------------------------------------------------------------------------
# Canned answers
# ---------------------------------------------------------------------------

def ripe_obj(kind: str, attrs: list[tuple[str, str]]) -> str:
    return json.dumps({"objects": {"object": [{"type": kind, "attributes": {"attribute": [{"name": n, "value": v} for n, v in attrs]}}]}})


def ripestat(data: dict) -> str:
    return json.dumps({"status": "ok", "data": data})


def doh(rcode: int, rtype: int, answers: list[str]) -> str:
    return json.dumps({"Status": rcode, "Answer": [{"name": "x", "type": rtype, "TTL": 60, "data": a} for a in answers]})


def radb_page(objects: list[str]) -> str:
    return "<html>" + "".join(f'<pre class="query-result "><code>{o}</code></pre>' for o in objects) + "</html>"


def all_good() -> dict[str, tuple[int, str]]:
    """Every endpoint answering exactly the desired state."""
    return {
        f"rpki-validation/data.json?resource=AS215520&prefix={V6.replace(':', '%3A').replace('/', '%2F')}":
            (200, ripestat({"status": "valid", "validating_roas": [{"origin": "215520", "prefix": V6, "validity": "valid", "max_length": 48}]})),
        # ADR 0013: no ROA is possible for the /24; the expected state is not-found.
        "rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0%2F24":
            (200, ripestat({"status": "not-found", "validating_roas": []})),
        "routing-status/data.json?resource=44.32.58.0%2F24":
            (200, ripestat({"visibility": {"v4": {"ris_peers_seeing": 330, "total_ris_peers": 340}, "v6": {"ris_peers_seeing": 0, "total_ris_peers": 0}},
                            "origins": [{"origin": 215520, "route_objects": ["RADB"]}]})),
        f"routing-status/data.json?resource={V6.replace(':', '%3A').replace('/', '%2F')}":
            (200, ripestat({"visibility": {"v4": {"ris_peers_seeing": 0, "total_ris_peers": 0}, "v6": {"ris_peers_seeing": 320, "total_ris_peers": 320}},
                            "origins": [{"origin": 215520, "route_objects": ["RADB", "RIPE"]}]})),
        "announced-prefixes/data.json?resource=AS215520":
            (200, ripestat({"latest_time": "T", "prefixes": [
                {"prefix": V6, "timelines": [{"starttime": "S", "endtime": "T"}]},
                {"prefix": V4, "timelines": [{"starttime": "S", "endtime": "T"}]}]})),
        "rest.db.ripe.net/ripe/aut-num/AS215520.json":
            (200, ripe_obj("aut-num", [("aut-num", "AS215520"), ("import", "from AS20473 accept ANY"), ("export", "to AS20473 announce AS215520:AS-ALL"),
                                       ("mp-import", "afi ipv6.unicast from AS20473 accept ANY"), ("mp-import", "afi ipv6.unicast from AS835 accept ANY"),
                                       ("mnt-by", "JOSHBOYD-MNT")])),
        "rest.db.ripe.net/ripe/as-set/AS215520:AS-ALL.json":
            (200, ripe_obj("as-set", [("as-set", "AS215520:AS-ALL"), ("members", "AS215520"), ("mnt-by", "JOSHBOYD-MNT")])),
        "rest.db.ripe.net/ripe/route6/2a0f:85c1:368::/48AS215520.json":
            (200, ripe_obj("route6", [("route6", V6), ("origin", "AS215520"), ("mnt-by", "JOSHBOYD-MNT")])),
        "rest.db.ripe.net/ripe/inet6num/2a0f:85c1:368::/48.json":
            (200, ripe_obj("inet6num", [("inet6num", V6), ("mnt-by", "INFERNO-MNT"), ("mnt-routes", "JOSHBOYD-MNT")])),
        "radb.net/query?keywords=44.32.58.0%2F24":
            (200, radb_page(["route:          44.0.0.0/9\norigin:         AS7377\nmnt-by:         MAINT-ARDC",
                             "route:          44.32.58.0/24\norigin:         AS215520\ndescr:          KN4LJL\nmnt-by:         MAINT-ARDC"])),
        "peeringdb.com/api/net?asn=215520":
            (200, json.dumps({"data": [{"id": 35354, "irr_as_set": "RIPE::AS215520:AS-ALL", "info_prefixes4": 1, "info_prefixes6": 1, "policy_general": "Open"}]})),
        "peeringdb.com/api/poc?net_id=35354":
            (200, json.dumps({"data": [{"role": "Abuse"}, {"role": "Technical"}, {"role": "Policy"}]})),
        f"dns-query?name={ZONE}&type=NS": (200, doh(0, 2, CF_NS)),
        f"dns-query?name={ZONE}&type=DS": (200, doh(0, 43, ["2371 13 2 abcdef"])),
        f"dns-query?name=git.{ZONE}&type=A": (200, doh(0, 1, ["44.32.58.1"])),
        f"dns-query?name=git.{ZONE}&type=AAAA": (200, doh(0, 28, ["2a0f:85c1:368:1::1"])),
        "dns-query?name=58.32.44.in-addr.arpa&type=NS": (200, doh(0, 2, CF_NS)),
        "dns-query?name=8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa&type=NS": (200, doh(0, 2, CF_NS)),
    }


class FakeFetch:
    """Maps URL substrings to (status, body); unknown URLs raise FetchError."""

    def __init__(self, table: dict[str, tuple[int, str]]):
        self.table = table
        self.calls: list[str] = []

    def __call__(self, url: str, accept: str = "application/json", headers=None):
        self.calls.append(url)
        # longest matching key wins, so "type=A" never shadows "type=AAAA"
        matches = sorted((k for k in self.table if k in url), key=len, reverse=True)
        if matches:
            return self.table[matches[0]]
        raise nc.FetchError(f"no canned answer for {url}")


def run_checks(table: dict, expect: str = "announced", plan: dict | None = None, roas: dict | None = None, offline: bool = False):
    plan = plan or copy.deepcopy(PLAN_DATA)
    roas = roas or copy.deepcopy(ROAS_DATA)
    prefixes = [p["prefix"] for p in plan["bgp"]["prefixes"]]
    ctx = nc.Context(plan, roas, nc.parse_expect(expect, prefixes))
    fake = FakeFetch(table)
    original = nc.fetch
    nc.fetch = fake
    try:
        results = nc.run(ctx, offline)
    finally:
        nc.fetch = original
    return {r.name: r for r in results}, fake


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

class OfflineMode(unittest.TestCase):
    def test_repo_yaml_is_consistent(self):
        results, fake = run_checks({}, offline=True)
        self.assertEqual(results["yaml:consistency"].status, nc.OK)
        self.assertEqual(results["live"].status, nc.SKIP)
        self.assertEqual(fake.calls, [], "offline mode must not touch the network")
        self.assertEqual(nc.exit_code(list(results.values())), 0)

    def test_offline_flags_roa_maxlength_drift(self):
        roas = copy.deepcopy(ROAS_DATA)
        roas["roas"][1]["max_length"] = 25
        results, _ = run_checks({}, roas=roas, offline=True)
        self.assertEqual(results["yaml:consistency"].status, nc.DRIFT)
        self.assertIn("max_length", results["yaml:consistency"].actual)

    def test_offline_flags_roa_not_in_plan(self):
        roas = copy.deepcopy(ROAS_DATA)
        roas["roas"].append({"prefix": "198.51.100.0/24", "max_length": 24, "asn": 215520, "ca": "ripe-hosted"})
        results, _ = run_checks({}, roas=roas, offline=True)
        self.assertEqual(results["yaml:consistency"].status, nc.DRIFT)

    def test_yaml_drift_short_circuits_live_checks(self):
        roas = copy.deepcopy(ROAS_DATA)
        roas["asn"] = 64512
        results, fake = run_checks(all_good(), roas=roas)
        self.assertEqual(results["yaml:consistency"].status, nc.DRIFT)
        self.assertEqual(fake.calls, [])

    def test_cli_offline(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = nc.main(["--offline", "--json"])
        self.assertEqual(code, 0)
        doc = json.loads(buf.getvalue())
        self.assertEqual(doc["summary"]["DRIFT"], 0)
        self.assertEqual(doc["expect"], {V4: "announced", V6: "announced"})


class AllGood(unittest.TestCase):
    def test_everything_ok_exits_zero(self):
        results, _ = run_checks(all_good())
        bad = {n: (r.status, r.actual, r.note) for n, r in results.items() if r.status != nc.OK}
        self.assertEqual(bad, {})
        self.assertEqual(nc.exit_code(list(results.values())), 0)
        for name in ("rpki:" + V4, "rpki:" + V6, "routing:" + V4, "ripe:aut-num", "ripe:as-set",
                     "radb:route:" + V4, "peeringdb:net", "peeringdb:pocs", f"dns:{ZONE}:DS",
                     "dns:58.32.44.in-addr.arpa:NS"):
            self.assertIn(name, results)


class Rpki(unittest.TestCase):
    def test_valid_roa(self):
        results, _ = run_checks(all_good())
        self.assertEqual(results["rpki:" + V6].status, nc.OK)
        self.assertIn("maxLength 48", results["rpki:" + V6].actual)

    def test_invalid_roa_is_drift(self):
        table = all_good()
        table["rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0%2F24"] = (200, ripestat({
            "status": "invalid", "validating_roas": [{"origin": "7377", "prefix": "44.0.0.0/9", "validity": "invalid_asn", "max_length": 24}]}))
        results, _ = run_checks(table)
        r = results["rpki:" + V4]
        self.assertEqual(r.status, nc.DRIFT)
        self.assertTrue(r.actual.startswith("invalid"))
        self.assertIn("AS7377", r.actual)

    def test_unknown_roa_is_drift(self):
        table = all_good()
        table["rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0%2F24"] = (200, ripestat({"status": "unknown", "validating_roas": []}))
        results, _ = run_checks(table)
        self.assertEqual(results["rpki:" + V4].status, nc.DRIFT)
        self.assertEqual(results["rpki:" + V4].actual, "unknown")

    def test_wrong_maxlength_is_drift(self):
        table = all_good()
        table["rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0%2F24"] = (200, ripestat({
            "status": "valid", "validating_roas": [{"origin": "215520", "prefix": V4, "validity": "valid", "max_length": 32}]}))
        results, _ = run_checks(table)
        self.assertEqual(results["rpki:" + V4].status, nc.DRIFT)


class Routing(unittest.TestCase):
    def test_expect_withdrawn_tolerates_stale_paths(self):
        table = all_good()
        table["routing-status/data.json?resource=44.32.58.0%2F24"] = (200, ripestat({
            "visibility": {"v4": {"ris_peers_seeing": 1, "total_ris_peers": 326}, "v6": {"ris_peers_seeing": 0, "total_ris_peers": 0}}, "origins": []}))
        results, _ = run_checks(table, expect=f"{V4}=withdrawn")
        self.assertEqual(results["routing:" + V4].status, nc.OK)
        self.assertIn("stale", results["routing:" + V4].note)
        self.assertEqual(results["routing:" + V6].status, nc.OK)  # default stays announced

    def test_expect_announced_but_withdrawn_is_drift(self):
        table = all_good()
        table["routing-status/data.json?resource=44.32.58.0%2F24"] = (200, ripestat({
            "visibility": {"v4": {"ris_peers_seeing": 0, "total_ris_peers": 326}, "v6": {"ris_peers_seeing": 0, "total_ris_peers": 0}}, "origins": []}))
        results, _ = run_checks(table, expect="announced")
        self.assertEqual(results["routing:" + V4].status, nc.DRIFT)

    def test_foreign_origin_is_drift(self):
        table = all_good()
        table["routing-status/data.json?resource=44.32.58.0%2F24"] = (200, ripestat({
            "visibility": {"v4": {"ris_peers_seeing": 300, "total_ris_peers": 326}, "v6": {"ris_peers_seeing": 0, "total_ris_peers": 0}},
            "origins": [{"origin": 64496, "route_objects": []}]}))
        results, _ = run_checks(table)
        self.assertEqual(results["routing:" + V4].status, nc.DRIFT)
        self.assertIn("AS64496", results["routing:" + V4].note)

    def test_unexpected_extra_prefix(self):
        table = all_good()
        table["announced-prefixes/data.json?resource=AS215520"] = (200, ripestat({"latest_time": "T", "prefixes": [
            {"prefix": V6, "timelines": [{"starttime": "S", "endtime": "T"}]},
            {"prefix": "2a12:bec4:19a3::/48", "timelines": [{"starttime": "S", "endtime": "T"}]},
            {"prefix": V4, "timelines": [{"starttime": "S", "endtime": "OLD"}]}]}))
        results, _ = run_checks(table)
        r = results["routing:AS215520-prefixes"]
        self.assertEqual(r.status, nc.DRIFT)
        self.assertIn("2a12:bec4:19a3::/48", r.note)

    def test_parse_expect_rejects_unknown_prefix(self):
        with self.assertRaises(SystemExit):
            nc.parse_expect("10.0.0.0/8=announced", [V4, V6])
        with self.assertRaises(SystemExit):
            nc.parse_expect("maybe", [V4, V6])


class RipeDb(unittest.TestCase):
    def test_missing_as_set_and_stale_aut_num(self):
        table = all_good()
        table["rest.db.ripe.net/ripe/as-set/AS215520:AS-ALL.json"] = (404, json.dumps({"errormessages": {}}))
        table["rest.db.ripe.net/ripe/aut-num/AS215520.json"] = (200, ripe_obj("aut-num", [
            ("aut-num", "AS215520"), ("import", "from AS209735 accept ANY"), ("export", "to AS209735 announce AS215520")]))
        results, _ = run_checks(table)
        self.assertEqual(results["ripe:as-set"].status, nc.DRIFT)
        self.assertEqual(results["ripe:as-set"].actual, "object missing")
        r = results["ripe:aut-num"]
        self.assertEqual(r.status, nc.DRIFT)
        self.assertIn("missing AS835, AS20473", r.note)
        self.assertIn("AS209735", r.note)

    def test_mnt_routes_lost_is_drift(self):
        table = all_good()
        table["rest.db.ripe.net/ripe/inet6num/2a0f:85c1:368::/48.json"] = (200, ripe_obj("inet6num", [("inet6num", V6), ("mnt-by", "INFERNO-MNT")]))
        results, _ = run_checks(table)
        self.assertEqual(results["ripe:inet6num:" + V6].status, nc.DRIFT)


class Radb(unittest.TestCase):
    def test_parses_html(self):
        results, _ = run_checks(all_good())
        self.assertEqual(results["radb:route:" + V4].status, nc.OK)
        self.assertIn("KN4LJL", results["radb:route:" + V4].actual)

    def test_unparsable_html_is_unknown(self):
        table = all_good()
        table["radb.net/query?keywords=44.32.58.0%2F24"] = (200, "<html><body>Please enable JavaScript</body></html>")
        results, _ = run_checks(table)
        self.assertEqual(results["radb:route:" + V4].status, nc.UNKNOWN)
        self.assertIn("whois -h whois.radb.net", results["radb:route:" + V4].note)

    def test_wrong_origin_is_drift(self):
        table = all_good()
        table["radb.net/query?keywords=44.32.58.0%2F24"] = (200, radb_page(["route:          44.32.58.0/24\norigin:         AS64496\nmnt-by:         MAINT-ARDC"]))
        results, _ = run_checks(table)
        self.assertEqual(results["radb:route:" + V4].status, nc.DRIFT)


class PeeringDb(unittest.TestCase):
    def test_missing_fields(self):
        table = all_good()
        table["peeringdb.com/api/net?asn=215520"] = (200, json.dumps({"data": [{"id": 35354, "irr_as_set": "", "info_prefixes4": 100, "info_prefixes6": 100, "policy_general": "Open"}]}))
        table["peeringdb.com/api/poc?net_id=35354"] = (200, json.dumps({"data": []}))
        results, _ = run_checks(table)
        net = results["peeringdb:net"]
        self.assertEqual(net.status, nc.DRIFT)
        self.assertIn("irr_as_set=(empty)", net.note)
        self.assertIn("info_prefixes4=100", net.note)
        pocs = results["peeringdb:pocs"]
        self.assertEqual(pocs.status, nc.DRIFT)
        self.assertIn("Abuse", pocs.note)

    def test_partial_pocs(self):
        table = all_good()
        table["peeringdb.com/api/poc?net_id=35354"] = (200, json.dumps({"data": [{"role": "Abuse"}]}))
        results, _ = run_checks(table)
        self.assertEqual(results["peeringdb:pocs"].status, nc.DRIFT)
        self.assertEqual(results["peeringdb:pocs"].note, "missing or not public: Technical, Policy")

    def test_throttled_is_unknown_not_drift(self):
        table = all_good()
        table["peeringdb.com/api/net?asn=215520"] = (429, json.dumps({"message": "Request was throttled."}))
        results, _ = run_checks(table)
        self.assertEqual(results["peeringdb:net"].status, nc.UNKNOWN)
        self.assertEqual(results["peeringdb:pocs"].status, nc.UNKNOWN)
        self.assertIn("PEERINGDB_API_KEY", results["peeringdb:net"].note)

    def test_api_key_header(self):
        os.environ["PEERINGDB_API_KEY"] = "test-key"
        try:
            self.assertEqual(nc.peeringdb_headers(), {"Authorization": "Api-Key test-key"})
        finally:
            del os.environ["PEERINGDB_API_KEY"]
        self.assertEqual(nc.peeringdb_headers(), {})


class Dns(unittest.TestCase):
    def test_dns_drift(self):
        table = all_good()
        table[f"dns-query?name={ZONE}&type=DS"] = (200, json.dumps({"Status": 0, "Authority": []}))
        table[f"dns-query?name=git.{ZONE}&type=A"] = (200, doh(0, 1, ["207.246.80.52"]))
        table[f"dns-query?name=git.{ZONE}&type=AAAA"] = (200, json.dumps({"Status": 0}))
        table["dns-query?name=58.32.44.in-addr.arpa&type=NS"] = (200, json.dumps({"Status": 3, "Authority": []}))
        table["dns-query?name=8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa&type=NS"] = (200, doh(0, 2, ["ns1.example.net.", "ns2.example.net."]))
        results, _ = run_checks(table)
        self.assertEqual(results[f"dns:{ZONE}:DS"].status, nc.DRIFT)
        self.assertIn("Namecheap", results[f"dns:{ZONE}:DS"].note)
        self.assertEqual(results[f"dns:git.{ZONE}:A"].status, nc.DRIFT)
        self.assertEqual(results[f"dns:git.{ZONE}:A"].actual, "207.246.80.52")
        self.assertEqual(results[f"dns:git.{ZONE}:AAAA"].status, nc.DRIFT)
        self.assertEqual(results["dns:58.32.44.in-addr.arpa:NS"].status, nc.DRIFT)
        self.assertIn("ARDC", results["dns:58.32.44.in-addr.arpa:NS"].note)
        self.assertEqual(results["dns:8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa:NS"].status, nc.DRIFT)
        self.assertEqual(results["dns:8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa:NS"].note, "delegated elsewhere")
        self.assertEqual(nc.exit_code(list(results.values())), 1)

    def test_zone_moved_off_cloudflare(self):
        table = all_good()
        table[f"dns-query?name={ZONE}&type=NS"] = (200, doh(0, 2, ["dns1.registrar-servers.com."]))
        results, _ = run_checks(table)
        self.assertEqual(results[f"dns:{ZONE}:NS"].status, nc.DRIFT)
        # reverse zones still checked (delegated somewhere), just without an NS-set comparison
        self.assertEqual(results["dns:58.32.44.in-addr.arpa:NS"].status, nc.OK)

    def test_aaaa_compares_normalised(self):
        table = all_good()
        table[f"dns-query?name=git.{ZONE}&type=AAAA"] = (200, doh(0, 28, ["2A0F:85C1:0368:0001:0000:0000:0000:0001"]))
        results, _ = run_checks(table)
        self.assertEqual(results[f"dns:git.{ZONE}:AAAA"].status, nc.OK)


class Robustness(unittest.TestCase):
    def test_network_failure_is_unknown_and_exit_2(self):
        results, _ = run_checks({})  # every fetch raises FetchError
        statuses = {r.status for n, r in results.items() if n != "yaml:consistency"}
        self.assertEqual(statuses, {nc.UNKNOWN})
        self.assertEqual(nc.exit_code(list(results.values())), 2)
        self.assertTrue(all("fetch failed" in r.note for n, r in results.items() if n != "yaml:consistency"))

    def test_garbage_json_is_unknown(self):
        table = all_good()
        table["rest.db.ripe.net/ripe/aut-num/AS215520.json"] = (200, "<html>maintenance</html>")
        table[f"rpki-validation/data.json?resource=AS215520&prefix={V6.replace(':', '%3A').replace('/', '%2F')}"] = (200, json.dumps({"data": {}}))
        results, _ = run_checks(table)
        self.assertEqual(results["ripe:aut-num"].status, nc.UNKNOWN)
        self.assertEqual(results["rpki:" + V6].status, nc.UNKNOWN)

    def test_table_rendering_and_exit_codes(self):
        results, _ = run_checks(all_good())
        text = nc.render_table(list(results.values()))
        self.assertIn("CHECK", text.splitlines()[0])
        self.assertIn("rpki:" + V6, text)
        self.assertEqual(nc.exit_code([nc.Result("a", nc.OK), nc.Result("b", nc.DRIFT), nc.Result("c", nc.UNKNOWN)]), 1)
        self.assertEqual(nc.exit_code([nc.Result("a", nc.OK), nc.Result("c", nc.UNKNOWN)]), 2)
        self.assertEqual(nc.exit_code([nc.Result("a", nc.OK), nc.Result("d", nc.SKIP)]), 0)


if __name__ == "__main__":
    unittest.main()
