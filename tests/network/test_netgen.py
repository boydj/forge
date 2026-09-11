"""Tests for scripts/netgen and the generated network configuration.

Run with either:
    python3 -m unittest discover -s tests/network -v
    python3 -m pytest tests/network

Only the standard library and PyYAML are needed. `bird -p` validation runs when
a BIRD 2 binary is found (PATH, or ~/.local/bird2/usr/sbin/bird) and is skipped
otherwise.
"""

from __future__ import annotations

import copy
import filecmp
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), os.pardir, os.pardir))
NETGEN = os.path.join(REPO, "scripts", "netgen")
PLAN = os.path.join(REPO, "infra", "network", "address-plan.yaml")
OVERRIDES = os.path.join(REPO, "infra", "network", "overrides.example.yaml")
BGP_ANNOUNCE = os.path.join(REPO, "scripts", "bgp-announce")
FORBIDDEN = re.compile(r"20473:6000\b|\(\s*20473\s*,\s*6000\s*\)")


def run_netgen(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    proc = subprocess.run([sys.executable, NETGEN, *args], capture_output=True, text=True)
    if check and proc.returncode != 0:
        raise AssertionError(f"netgen {' '.join(args)} failed:\n{proc.stdout}\n{proc.stderr}")
    return proc


def find_bird() -> str | None:
    for candidate in (shutil.which("bird"), os.path.expanduser("~/.local/bird2/usr/sbin/bird")):
        if candidate and os.access(candidate, os.X_OK):
            return candidate
    return None


def tree(root: str) -> dict[str, str]:
    out = {}
    for dirpath, _, files in os.walk(root):
        for f in files:
            full = os.path.join(dirpath, f)
            with open(full, encoding="utf-8") as fh:
                out[os.path.relpath(full, root)] = fh.read()
    return out


def parse_wg(text: str) -> tuple[dict[str, str], list[dict[str, str]]]:
    interface: dict[str, str] = {}
    peers: list[dict[str, str]] = []
    current: dict[str, str] | None = None
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line == "[Interface]":
            current = interface
        elif line == "[Peer]":
            current = {}
            peers.append(current)
        else:
            key, _, value = line.partition("=")
            assert current is not None, f"key outside a section: {line}"
            current[key.strip()] = value.strip()
    return interface, peers


class NetgenTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.tmp = tempfile.mkdtemp(prefix="netgen-test-")
        cls.out1 = os.path.join(cls.tmp, "run1")
        cls.out2 = os.path.join(cls.tmp, "run2")
        run_netgen("--overrides", OVERRIDES, "--out-root", cls.out1, "-q")
        run_netgen("--overrides", OVERRIDES, "--out-root", cls.out2, "-q")
        with open(PLAN, encoding="utf-8") as fh:
            cls.plan = yaml.safe_load(fh)
        with open(OVERRIDES, encoding="utf-8") as fh:
            cls.overrides = yaml.safe_load(fh)
        cls.pops = {p["name"]: p for p in cls.plan["pops"]}
        cls.files = tree(cls.out1)

    @classmethod
    def tearDownClass(cls) -> None:
        shutil.rmtree(cls.tmp, ignore_errors=True)

    # -- plan validation ----------------------------------------------------

    def test_check_passes(self) -> None:
        proc = run_netgen("--check")
        self.assertIn("netgen --check: OK", proc.stdout)
        proc = run_netgen("--check", "--overrides", OVERRIDES)
        self.assertIn("netgen --check: OK", proc.stdout)

    def _check_broken(self, mutate, expect: str) -> None:
        plan = copy.deepcopy(self.plan)
        mutate(plan)
        path = os.path.join(self.tmp, "broken.yaml")
        with open(path, "w", encoding="utf-8") as fh:
            yaml.safe_dump(plan, fh)
        proc = run_netgen("--check", "--plan", path, check=False)
        self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
        self.assertIn(expect, proc.stderr)

    def test_check_rejects_duplicate_index(self) -> None:
        def mutate(plan):
            plan["pops"][1]["index"] = plan["pops"][0]["index"]
        self._check_broken(mutate, "duplicate index")

    def test_check_rejects_roa_maxlength_mismatch(self) -> None:
        def mutate(plan):
            plan["rpki"]["roas"][0]["max_length"] = 64
        self._check_broken(mutate, "max_length must equal prefix length")

    def test_check_rejects_service_outside_prefix(self) -> None:
        def mutate(plan):
            plan["ipv4"]["services"][0]["address"] = "44.32.59.1"
        self._check_broken(mutate, "is outside")

    def test_check_rejects_overlapping_subnets(self) -> None:
        def mutate(plan):
            plan["ipv6"]["subnets"].append({"prefix": "2a0f:85c1:368:1::/64", "purpose": "dup"})
        self._check_broken(mutate, "overlaps")

    def test_check_rejects_forbidden_community(self) -> None:
        def mutate(plan):
            plan["bgp"]["upstreams"]["vultr"]["export_communities"] = ["20473:6000"]
        self._check_broken(mutate, "20473:6000")

    def test_check_rejects_wrong_derived_block(self) -> None:
        def mutate(plan):
            plan["pops"][0]["ipv6_unicast_block"] = "2a0f:85c1:368:1ab::/64"
        self._check_broken(mutate, "!= derived")

    def test_overrides_unknown_pop_rejected(self) -> None:
        path = os.path.join(self.tmp, "bad-overrides.yaml")
        with open(path, "w", encoding="utf-8") as fh:
            yaml.safe_dump({"pops": {"nope": {"provider_ipv4": "203.0.113.1"}}}, fh)
        proc = run_netgen("--check", "--overrides", path, check=False)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unknown POP", proc.stderr)

    # -- generation ---------------------------------------------------------

    def test_generation_is_deterministic(self) -> None:
        cmp = filecmp.dircmp(self.out1, self.out2)
        diffs: list[str] = []

        def collect(dc: filecmp.dircmp, prefix: str = "") -> None:
            diffs.extend(prefix + f for f in dc.diff_files + dc.left_only + dc.right_only + dc.funny_files)
            for name, sub in dc.subdirs.items():
                collect(sub, prefix + name + "/")

        collect(cmp)
        self.assertEqual(diffs, [], f"two runs differ: {diffs}")
        self.assertEqual(tree(self.out1), tree(self.out2))

    def test_expected_files_exist(self) -> None:
        for pop in self.pops:
            self.assertIn(f"infra/bird/generated/{pop}/bird.conf", self.files)
            self.assertIn(f"infra/bird/generated/{pop}/state.conf", self.files)
            self.assertIn(f"infra/wireguard/generated/{pop}/wg0.conf", self.files)
        for name in ("dns-nodes.yaml", "nftables-vars.nft", "summary.md"):
            self.assertIn(f"infra/network/generated/{name}", self.files)

    def test_bird_exports_exactly_our_two_prefixes(self) -> None:
        for pop in self.pops:
            conf = self.files[f"infra/bird/generated/{pop}/bird.conf"]
            with self.subTest(pop=pop):
                statics = re.findall(r"^\s*route\s+(\S+)\s+unreachable;", conf, re.M)
                self.assertEqual(sorted(statics), sorted(["44.32.58.0/24", "2a0f:85c1:368::/48"]))
                allow = re.findall(r"^\s*if net != (\S+) then reject;", conf, re.M)
                self.assertEqual(sorted(allow), sorted(["44.32.58.0/24", "2a0f:85c1:368::/48"]))
                self.assertEqual(len(re.findall(r"^filter export_anycast[46] \{", conf, re.M)), 2)
                self.assertEqual(len(re.findall(r"^\s*if ! ANNOUNCE then reject;", conf, re.M)), 2)
                self.assertEqual(len(re.findall(r"^\s*if source != RTS_STATIC then reject;", conf, re.M)), 2)
                self.assertRegex(conf, r"import limit \d+ action block;")
                self.assertTrue(re.search(r"^\s*if net = 0\.0\.0\.0/0 then accept;", conf, re.M))
                self.assertTrue(re.search(r"^\s*if net = ::/0 then accept;", conf, re.M))
                self.assertFalse(re.search(r"^\s*protocol direct", conf, re.M))
                self.assertIn('include "state.conf";', conf)
                self.assertIn("bgp_community.add((65535, 0));", conf)

    def test_no_forbidden_community_anywhere(self) -> None:
        for rel, content in self.files.items():
            self.assertIsNone(FORBIDDEN.search(content), f"{rel} contains 20473:6000")
        with open(PLAN, encoding="utf-8") as fh:
            plan_text = fh.read()
        # The plan may name it only in the "never set" documentation contexts.
        for line in plan_text.splitlines():
            if "20473:6000" in line:
                self.assertTrue(
                    "forbidden" in line or "never" in line or line.lstrip().startswith("#"),
                    f"plan line mentions 20473:6000 outside a forbidden/never context: {line}",
                )

    def test_bird_uses_provider_addresses_from_overrides(self) -> None:
        for pop, ov in self.overrides["pops"].items():
            conf = self.files[f"infra/bird/generated/{pop}/bird.conf"]
            self.assertIn(f"router id {ov['provider_ipv4']};", conf)
            self.assertIn(f"local {ov['provider_ipv4']} as 215520;", conf)
            self.assertIn(f"local {ov['provider_ipv6']} as 215520;", conf)
            self.assertNotIn("__PROVIDER_IP", conf)

    def test_bird_placeholders_without_overrides(self) -> None:
        out = os.path.join(self.tmp, "no-overrides")
        run_netgen("--out-root", out, "-q")
        conf = tree(out)["infra/bird/generated/ewr1/bird.conf"]
        self.assertIn("router id __PROVIDER_IPV4__;", conf)
        wg = tree(out)["infra/wireguard/generated/ewr1/wg0.conf"]
        # Peers without a known public key are left out (commented) so the
        # file stays loadable by wg-quick on a partially provisioned mesh.
        self.assertNotIn("__WG_PUBKEY_", wg)
        self.assertIn("# [Peer] ams1: not yet provisioned", wg)
        self.assertNotIn("__WG_ENDPOINT_", wg)

    def test_state_conf_initially_withdrawn(self) -> None:
        for pop in self.pops:
            state = self.files[f"infra/bird/generated/{pop}/state.conf"]
            self.assertIn("define ANNOUNCE = false;", state)
            self.assertIn("define DRAIN = false;", state)

    def test_bird_syntax(self) -> None:
        bird = find_bird()
        if bird is None:
            self.skipTest("bird binary not available")
        for pop in self.pops:
            conf_dir = os.path.join(self.out1, "infra", "bird", "generated", pop)
            conf = os.path.join(conf_dir, "bird.conf")
            with self.subTest(pop=pop):
                proc = subprocess.run([bird, "-p", "-c", conf], capture_output=True, text=True)
                self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                # Also parse with announce+drain active: the drain block must be valid.
                state = os.path.join(conf_dir, "state.conf")
                with open(state, encoding="utf-8") as fh:
                    original = fh.read()
                try:
                    with open(state, "w", encoding="utf-8") as fh:
                        fh.write("define ANNOUNCE = true;\ndefine DRAIN = true;\n")
                    proc = subprocess.run([bird, "-p", "-c", conf], capture_output=True, text=True)
                    self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                finally:
                    with open(state, "w", encoding="utf-8") as fh:
                        fh.write(original)

    # -- WireGuard ----------------------------------------------------------

    def test_wireguard_mesh_pairwise_consistent(self) -> None:
        wg = self.plan["wireguard"]
        configs = {}
        for pop in self.pops:
            configs[pop] = parse_wg(self.files[f"infra/wireguard/generated/{pop}/wg0.conf"])
        groups: dict[str, list[str]] = {}
        for name, p in self.pops.items():
            g = "lab" if ("lab" in p["roles"] or p["provider"] == "local") else "production"
            groups.setdefault(g, []).append(name)
        for name, (iface, peers) in configs.items():
            p = self.pops[name]
            self.assertEqual(iface["Address"], f"{p['wg_address']}/64")
            self.assertEqual(iface["ListenPort"], str(p["wg_listen_port"]))
            self.assertEqual(iface["PrivateKey"], "__WG_PRIVATE_KEY__")
            self.assertEqual(iface["MTU"], str(wg["mtu"]))
            group = "lab" if ("lab" in p["roles"] or p["provider"] == "local") else "production"
            expected_peers = sorted(n for n in groups[group] if n != name)
            by_key = {}
            for peer in peers:
                by_key[peer["PublicKey"]] = peer
            self.assertEqual(len(peers), len(expected_peers), f"{name}: peer count")
            for other in expected_peers:
                op = self.pops[other]
                ov = self.overrides["pops"].get(other, {})
                pub = self.overrides["wireguard"]["public_keys"].get(other, f"__WG_PUBKEY_{other}__")
                self.assertIn(pub, by_key, f"{name}: missing peer {other}")
                peer = by_key[pub]
                self.assertEqual(peer["Endpoint"], f"[{ov['provider_ipv6']}]:{op['wg_listen_port']}")
                allowed = {a.strip() for a in peer["AllowedIPs"].split(",")}
                self.assertEqual(allowed, {f"{op['wg_address']}/128", op["ipv6_unicast_block"]})
                self.assertEqual(peer["PersistentKeepalive"], str(wg["persistent_keepalive"]))
        # Symmetry: if a lists b then b lists a (same group), with identical endpoint semantics.
        for a in self.pops:
            for b in self.pops:
                if a == b:
                    continue
                a_has_b = any(self.pops[b]["wg_address"] in peer["AllowedIPs"] for peer in configs[a][1])
                b_has_a = any(self.pops[a]["wg_address"] in peer["AllowedIPs"] for peer in configs[b][1])
                self.assertEqual(a_has_b, b_has_a, f"asymmetric mesh between {a} and {b}")

    # -- DNS / nftables -----------------------------------------------------

    def test_dns_nodes(self) -> None:
        doc = yaml.safe_load(self.files["infra/network/generated/dns-nodes.yaml"])
        self.assertEqual(doc["zone_name"], "as215520.net")
        self.assertEqual(doc["anycast_v4"], ["44.32.58.1"])
        self.assertEqual(doc["anycast_v6"], ["2a0f:85c1:368:1::1"])
        self.assertNotIn("lab", doc["nodes"])
        self.assertEqual(doc["nodes"]["ewr1"], {"ipv4": "203.0.113.10", "ipv6": "2a0f:85c1:368:101::1"})
        self.assertEqual(doc["nodes"]["ams1"]["ipv6"], "2a0f:85c1:368:102::1")

    def test_nftables_vars(self) -> None:
        nft = self.files["infra/network/generated/nftables-vars.nft"]
        self.assertIn("define anycast_v4 = { 44.32.58.1 }", nft)
        self.assertIn("define anycast_v6 = { 2a0f:85c1:368:1::1 }", nft)
        self.assertIn("define bgp_peers_v4 = { 169.254.169.254 }", nft)
        self.assertIn("define bgp_peers_v6 = { 2001:19f0:ffff::1 }", nft)
        self.assertIn("define wg_port = 51820", nft)
        self.assertRegex(nft, r"define wg_nodes_v6 = \{ fda5:bc65:9bb1:1::[12], fda5:bc65:9bb1:1::[12] \}")
        self.assertNotIn("1fe::", nft)  # lab never appears in the production firewall sets


class BgpAnnounceTest(unittest.TestCase):
    """bgp-announce state-file logic, with BIRD reload disabled."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="bgp-announce-")
        self.state = os.path.join(self.tmp, "state.conf")
        self.env = dict(os.environ, BGP_STATE_FILE=self.state, BGP_NO_RELOAD="1")

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_cmd(self, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run([BGP_ANNOUNCE, *args], capture_output=True, text=True, env=self.env)

    def read(self) -> str:
        with open(self.state, encoding="utf-8") as fh:
            return fh.read()

    def test_transitions(self) -> None:
        self.assertIn("state: withdrawn", self.run_cmd("status").stdout)
        self.assertEqual(self.run_cmd("drain").returncode, 1)  # cannot drain while withdrawn
        self.assertEqual(self.run_cmd("announce").returncode, 0)
        self.assertIn("define ANNOUNCE = true;", self.read())
        self.assertIn("unchanged", self.run_cmd("announce").stdout)
        self.assertEqual(self.run_cmd("drain").returncode, 0)
        self.assertIn("define DRAIN = true;", self.read())
        self.assertIn("state: draining", self.run_cmd("status").stdout)
        self.assertEqual(self.run_cmd("undrain").returncode, 0)
        self.assertIn("define DRAIN = false;", self.read())
        self.assertEqual(self.run_cmd("withdraw").returncode, 0)
        self.assertIn("define ANNOUNCE = false;", self.read())
        self.assertEqual(self.run_cmd("bogus").returncode, 2)
        self.assertEqual([f for f in os.listdir(self.tmp) if f != "state.conf"], [], "temp files left behind")

    def test_state_file_parses_in_bird(self) -> None:
        bird = find_bird()
        if bird is None:
            self.skipTest("bird binary not available")
        out = os.path.join(self.tmp, "gen")
        run_netgen("--overrides", OVERRIDES, "--out-root", out, "-q")
        conf_dir = os.path.join(out, "infra", "bird", "generated", "ewr1")
        env = dict(os.environ, BGP_STATE_FILE=os.path.join(conf_dir, "state.conf"), BGP_NO_RELOAD="1")
        for cmd in ("announce", "drain", "undrain", "withdraw"):
            subprocess.run([BGP_ANNOUNCE, cmd], check=True, capture_output=True, env=env)
            proc = subprocess.run([bird, "-p", "-c", os.path.join(conf_dir, "bird.conf")], capture_output=True, text=True)
            self.assertEqual(proc.returncode, 0, f"after {cmd}: {proc.stdout}{proc.stderr}")


if __name__ == "__main__":
    unittest.main()
