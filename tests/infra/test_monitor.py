"""Validate the monitoring host as code (docs/monitoring.md) without a provider.

* `infra/cloud-init/monitor.yaml.tftpl` renders (same placeholder emulation
  as test_cloud_init), parses as YAML, installs from Debian only, ships no
  forge/BIRD pieces, keeps sshd tunnel-only, and stays under a line cap;
* `infra/firewall/nftables-monitor.conf.tftpl` renders with no public
  service ports;
* `infra/monitoring/prometheus/prometheus.yml.tftpl` renders through a real
  `tofu` (templatefile has %{ for } directives this test does not emulate)
  for a POP list and for the empty list, the result is valid YAML with the
  expected jobs and labels, and `promtool check config` accepts it;
* the alert rules pass `promtool check rules` and their unit tests;
* the Grafana pin (apt-repo.conf), the systemd drop-in and the provisioning
  files say what `scripts/deploy monitor` relies on.

tofu/promtool are used when found (PATH or ~/.local/bin) and skipped
otherwise, like bird in tests/network.

Run: python3 -m unittest discover -s tests/infra
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

try:
    import yaml
except ImportError:  # pragma: no cover
    yaml = None

from test_cloud_init import ROOT, read, render  # noqa: E402  (same start directory)

MONITOR_CLOUD_INIT = os.path.join(ROOT, "infra", "cloud-init", "monitor.yaml.tftpl")
NFT_MONITOR = os.path.join(ROOT, "infra", "firewall", "nftables-monitor.conf.tftpl")
PROM_TEMPLATE = os.path.join(ROOT, "infra", "monitoring", "prometheus", "prometheus.yml.tftpl")
RULES_DIR = os.path.join(ROOT, "infra", "monitoring", "prometheus", "rules")
RULES = os.path.join(RULES_DIR, "forge.yml")
RULES_TEST = os.path.join(RULES_DIR, "forge_test.yml")
BLACKBOX = os.path.join(ROOT, "infra", "monitoring", "blackbox.yml")
GRAFANA = os.path.join(ROOT, "infra", "monitoring", "grafana")
DEPLOY = os.path.join(ROOT, "scripts", "deploy")
# Small on purpose: the monitoring payloads travel with `scripts/deploy monitor`.
MAX_LINES = 200


def find_tool(name):
    for candidate in (shutil.which(name), os.path.expanduser(f"~/.local/bin/{name}")):
        if candidate and os.access(candidate, os.X_OK):
            return candidate
    return None


def render_nft_monitor():
    return render(
        read(NFT_MONITOR),
        {"admin_ssh_port": 2200, "wg_port": 51820, "wg_iface": "wg0", "mesh_tcp_ports": "9090, 3000"},
        {},
    )


def hcl(value):
    """Python -> HCL literal for the values the Prometheus template takes."""
    if isinstance(value, str):
        return json.dumps(value)
    if isinstance(value, list):
        return "[" + ", ".join(hcl(v) for v in value) + "]"
    if isinstance(value, dict):
        return "{ " + ", ".join(f"{k} = {hcl(v)}" for k, v in value.items()) + " }"
    raise TypeError(type(value))


def render_prometheus(tofu, variables):
    """templatefile() through tofu itself: an output-only root module, applied in a temp dir."""
    tmp = tempfile.mkdtemp(prefix="prom-tpl-")
    try:
        with open(os.path.join(tmp, "main.tf"), "w", encoding="utf-8") as fh:
            fh.write('output "cfg" {\n  value = templatefile(%s, %s)\n}\n' % (json.dumps(PROM_TEMPLATE), hcl(variables)))
        env = dict(os.environ, TF_IN_AUTOMATION="1")
        for args in (["init", "-backend=false", "-input=false"], ["apply", "-auto-approve", "-input=false"]):
            proc = subprocess.run([tofu, f"-chdir={tmp}", *args], capture_output=True, text=True, env=env)
            if proc.returncode != 0:
                raise AssertionError(f"tofu {args[0]} failed:\n{proc.stdout}\n{proc.stderr}")
        proc = subprocess.run([tofu, f"-chdir={tmp}", "output", "-raw", "cfg"], capture_output=True, text=True, env=env)
        if proc.returncode != 0:
            raise AssertionError(f"tofu output failed:\n{proc.stdout}\n{proc.stderr}")
        return proc.stdout
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


POPS = [
    {"name": "ewr1", "wg_address": "fda5:bc65:9bb1:1::1", "unicast_v6": "2a0f:85c1:368:101::1",
     "provider_ipv4": "203.0.113.10", "provider_ipv6": "2001:db8:1::10"},
    {"name": "ams1", "wg_address": "fda5:bc65:9bb1:1::2", "unicast_v6": "2a0f:85c1:368:102::1",
     "provider_ipv4": "203.0.113.20", "provider_ipv6": "2001:db8:2::20"},
]
PROM_VARS = {
    "monitor_pop": "mon1", "monitor_node": "mon1", "service_hostname": "git.example.org",
    "anycast_v4": "44.32.58.1", "anycast_v6": "2a0f:85c1:368:1::1",
    "blackbox_address": "127.0.0.1:9115", "alertmanager_targets": [], "pops": POPS,
}


class MonitorCloudInit(unittest.TestCase):
    def setUp(self):
        self.template = read(MONITOR_CLOUD_INIT)
        self.blocks = {
            "nftables_conf": render_nft_monitor(),
            "operator_ssh_authorized_keys": "      - ssh-ed25519 AAAAexample operator",
        }
        self.scalars = {
            "node": "mon1", "hostname_fqdn": "mon1.nodes.example.org", "service_hostname": "git.example.org",
            "pop_index": 250, "admin_ssh_port": 2200,
        }
        self.rendered = render(self.template, self.scalars, self.blocks)

    def test_size_and_directives(self):
        n = self.template.count("\n")
        self.assertLessEqual(n, MAX_LINES, f"monitor.yaml.tftpl is {n} lines; keep it under {MAX_LINES}")
        self.assertNotIn("%{", self.template)
        self.assertTrue(self.template.startswith("#cloud-config\n"))

    def test_no_unrendered_placeholders(self):
        flat = render(self.template, self.scalars, {k: "X" for k in self.blocks})
        self.assertNotIn("${", flat)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_yaml_structure(self):
        doc = yaml.safe_load(self.rendered)
        self.assertEqual(doc["hostname"], "mon1")
        self.assertFalse(doc["ssh_pwauth"])
        for pkg in ("prometheus", "promtool", "prometheus-blackbox-exporter", "wireguard-tools", "nftables", "gnupg", "unattended-upgrades"):
            self.assertIn(pkg, doc["packages"])
        for pkg in ("bird2", "git", "age", "grafana"):  # no BGP, no forge, no secret bundle; Grafana comes via deploy
            self.assertNotIn(pkg, doc["packages"])
        self.assertEqual([u["name"] for u in doc["users"]], ["deploy"])
        self.assertNotIn("sudo", doc["users"][0], "the monitor's deploy user has no sudo at all")
        self.assertNotIn("NOPASSWD", self.rendered)
        for item in doc["runcmd"]:
            self.assertIsInstance(item, list, f"runcmd entries must be argv lists: {item!r}")
        paths = [f["path"] for f in doc["write_files"]]
        for p in paths:
            self.assertFalse(p.startswith(("/usr/local/", "/etc/systemd/system/forge", "/etc/bird", "/etc/forge/forge.toml")), p)
        self.assertNotIn("/etc/forge/secrets", self.rendered)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_sshd_is_tunnel_only(self):
        doc = yaml.safe_load(self.rendered)
        files = {f["path"]: f for f in doc["write_files"]}
        sshd = files["/etc/ssh/sshd_config.d/10-forge.conf"]["content"]
        self.assertIn("Port 2200", sshd)
        self.assertIn("PasswordAuthentication no", sshd)
        self.assertIn("PermitRootLogin prohibit-password", sshd)
        self.assertIn("AllowTcpForwarding local", sshd)
        permit = re.search(r"(?m)^PermitOpen (.*)$", sshd)
        self.assertIsNotNone(permit, "PermitOpen must restrict the tunnels")
        for target in permit.group(1).split():
            self.assertTrue(target.startswith("127.0.0.1:"), target)
        self.assertIn("127.0.0.1:3000", permit.group(1))
        self.assertIn("127.0.0.1:9090", permit.group(1))

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_exporter_args_and_firewall(self):
        doc = yaml.safe_load(self.rendered)
        files = {f["path"]: f for f in doc["write_files"]}
        for path in ("/etc/nftables.conf", "/etc/default/prometheus", "/etc/default/prometheus-blackbox-exporter"):
            self.assertTrue(files[path].get("defer"), f"{path} is a package conffile; must use defer: true")
        prom = files["/etc/default/prometheus"]["content"]
        self.assertIn("--web.listen-address=[::]:9090", prom, "IPv6-only mesh must be able to reach Prometheus")
        self.assertIn("--storage.tsdb.retention", prom)
        self.assertNotIn("--web.enable-lifecycle", prom, "no unauthenticated reload/quit endpoint")
        bb = files["/etc/default/prometheus-blackbox-exporter"]["content"]
        self.assertIn("--config.file /etc/prometheus/blackbox.yml", bb)
        self.assertIn("--web.listen-address 127.0.0.1:9115", bb)
        self.assertEqual(files["/etc/nftables.conf"]["content"].rstrip("\n"), self.blocks["nftables_conf"].rstrip("\n"))
        joined = "\n".join(" ".join(map(str, c)) for c in doc["runcmd"])
        self.assertIn("nft -c -f /etc/nftables.conf", joined)
        self.assertIn("systemctl restart prometheus prometheus-blackbox-exporter", joined)
        self.assertIn("ssh_host_ed25519_key.pub", joined, "host key printed for pinning (SR-19b)")

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_no_downloads_in_runcmd(self):
        doc = yaml.safe_load(self.rendered)
        joined = " ".join(" ".join(map(str, c)) for c in doc["runcmd"])
        for bad in ("curl ", "wget ", "github.com", "http://", "https://", "apt.grafana.com"):
            self.assertNotIn(bad, joined, "cloud-init installs from Debian only; Grafana comes with scripts/deploy monitor")


class MonitorNftables(unittest.TestCase):
    def test_renders_without_public_services(self):
        out = render_nft_monitor()
        self.assertNotIn("${", out)
        for needle in ("policy drop", "ADMIN_SSH = 2200", "udp dport $WG_PORT", "iifname $WG_IF tcp dport $MESH_TCP", "MESH_TCP  = { 9090, 3000 }"):
            self.assertIn(needle, out)
        # No forge service ports, no BGP peers: those belong to a POP's ruleset.
        self.assertNotIn("1965", out)
        self.assertNotIn("dport 179", out)
        self.assertNotIn("dport { 22", out)
        self.assertNotIn("BGP_PEERS", out)
        self.assertRegex(out, r"chain forward \{\s*#[^\n]*\n\s*type filter hook forward priority filter; policy drop;")
        # `nft -c` needs CAP_NET_ADMIN even for a dry run, so the syntax check
        # happens on the host (cloud-init and scripts/deploy monitor both run
        # it before loading the ruleset), not here.


class PrometheusTemplate(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tofu = find_tool("tofu")
        cls.promtool = find_tool("promtool")
        cls.rendered = render_prometheus(cls.tofu, PROM_VARS) if cls.tofu else None

    def need_tofu(self):
        if not self.tofu:
            self.skipTest("tofu not available")

    def check_config(self, cfg):
        """promtool check config, with the rules next to the config like on the host."""
        if not self.promtool:
            return
        tmp = tempfile.mkdtemp(prefix="prom-check-")
        try:
            os.mkdir(os.path.join(tmp, "rules"))
            shutil.copy(RULES, os.path.join(tmp, "rules", "forge.yml"))
            with open(os.path.join(tmp, "prometheus.yml"), "w", encoding="utf-8") as fh:
                fh.write(cfg)
            proc = subprocess.run([self.promtool, "check", "config", "prometheus.yml"], cwd=tmp, capture_output=True, text=True)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_renders_pops(self):
        self.need_tofu()
        doc = yaml.safe_load(self.rendered)
        self.assertEqual(doc["global"]["external_labels"], {"monitor": "forge", "pop": "mon1", "node": "mon1"})
        self.assertEqual(doc["global"]["scrape_interval"], "30s")
        self.assertNotIn("alerting", doc, "no Alertmanager until a destination is configured")
        jobs = {j["job_name"]: j for j in doc["scrape_configs"]}
        for name in ("forge", "node", "bird", "prometheus", "blackbox_exporter",
                     "blackbox_gemini_v4", "blackbox_gemini_v6", "blackbox_ssh_v4", "blackbox_ssh_v6"):
            self.assertIn(name, jobs)
        # Mesh scrapes: one target per POP on the wg address, labelled pop/node (never instance).
        for job, port in (("forge", 9100), ("node", 9101), ("bird", 9324)):
            targets = {(sc["targets"][0], sc["labels"]["pop"], sc["labels"]["node"]) for sc in jobs[job]["static_configs"]}
            self.assertEqual(targets, {(f"[{p['wg_address']}]:{port}", p["name"], p["name"]) for p in POPS}, job)
        # Probes: the anycast address plus provider (v4/v6) and /48 unicast (v6) per POP.
        v6 = jobs["blackbox_gemini_v6"]["static_configs"]
        kinds = {(sc["labels"]["target_kind"], sc["labels"].get("path"), sc["targets"][0]) for sc in v6}
        self.assertIn(("anycast", None, "[2a0f:85c1:368:1::1]:1965"), kinds)
        self.assertIn(("node", "provider", "[2001:db8:1::10]:1965"), kinds)
        self.assertIn(("node", "unicast", "[2a0f:85c1:368:101::1]:1965"), kinds)
        v4 = jobs["blackbox_ssh_v4"]["static_configs"]
        self.assertIn("203.0.113.20:22", [sc["targets"][0] for sc in v4])
        self.assertNotIn("null", self.rendered)
        self.check_config(self.rendered)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_renders_without_pops(self):
        """create_node = false: no POP targets, still a valid configuration."""
        self.need_tofu()
        cfg = render_prometheus(self.tofu, dict(PROM_VARS, pops=[]))
        doc = yaml.safe_load(cfg)
        jobs = {j["job_name"]: j for j in doc["scrape_configs"]}
        # The template emits an empty `static_configs:` (YAML null) for the
        # mesh jobs; Prometheus treats that as no targets. promtool confirms.
        for job in ("forge", "node", "bird"):
            self.assertFalse(jobs[job].get("static_configs"), job)
        self.assertEqual(len(jobs["blackbox_gemini_v4"]["static_configs"]), 1)  # anycast only
        self.check_config(cfg)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_alertmanager_block(self):
        self.need_tofu()
        cfg = render_prometheus(self.tofu, dict(PROM_VARS, alertmanager_targets=["127.0.0.1:9093"]))
        doc = yaml.safe_load(cfg)
        self.assertEqual(doc["alerting"]["alertmanagers"][0]["static_configs"][0]["targets"], ["127.0.0.1:9093"])
        self.check_config(cfg)


class AlertRules(unittest.TestCase):
    def setUp(self):
        self.promtool = find_tool("promtool")
        if not self.promtool:
            self.skipTest("promtool not available")

    def test_rules_check(self):
        proc = subprocess.run([self.promtool, "check", "rules", RULES], capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_rules_unit_tests(self):
        proc = subprocess.run([self.promtool, "test", "rules", os.path.basename(RULES_TEST)], cwd=RULES_DIR, capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_blackbox_modules(self):
        doc = yaml.safe_load(read(BLACKBOX))
        for name in ("gemini_v4", "gemini_v6", "ssh_v4", "ssh_v6"):
            self.assertIn(name, doc["modules"])
            self.assertFalse(doc["modules"][name]["tcp"]["ip_protocol_fallback"], f"{name}: a v4 probe must not pass over v6")


class GrafanaAsCode(unittest.TestCase):
    def test_apt_repo_pin(self):
        conf = os.path.join(GRAFANA, "apt-repo.conf")
        text = read(conf)
        self.assertNotIn("$", text, "sourced by scripts/deploy: literal values only")
        proc = subprocess.run(["bash", "-c", f'set -e; . "{conf}"; printf "%s\\n" "$GRAFANA_APT_KEY_URL" "$GRAFANA_APT_KEY_FPR" "$GRAFANA_APT_REPO" "$GRAFANA_APT_PACKAGE"'],
                              capture_output=True, text=True, check=True)
        url, fpr, repo, pkg = proc.stdout.splitlines()
        self.assertTrue(url.startswith("https://apt.grafana.com/"), url)
        self.assertRegex(fpr, r"^[0-9A-F]{40}$", "primary key fingerprint, 40 hex digits")
        self.assertIn("signed-by=/etc/apt/keyrings/grafana.gpg", repo)
        self.assertIn("https://apt.grafana.com stable main", repo)
        self.assertEqual(pkg, "grafana")

    def test_systemd_dropin_is_loopback_only(self):
        text = read(os.path.join(GRAFANA, "grafana-server.override.conf"))
        self.assertIn("[Service]", text)
        env = dict(re.findall(r"(?m)^Environment=([A-Z_]+)=(\S+)$", text))
        self.assertEqual(env.get("GF_SERVER_HTTP_ADDR"), "127.0.0.1")
        self.assertEqual(env.get("GF_USERS_ALLOW_SIGN_UP"), "false")
        self.assertEqual(env.get("GF_AUTH_ANONYMOUS_ENABLED"), "false")
        self.assertEqual(env.get("GF_ANALYTICS_REPORTING_ENABLED"), "false")

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_provisioning(self):
        ds = yaml.safe_load(read(os.path.join(GRAFANA, "provisioning", "datasources", "prometheus.yml")))
        src = ds["datasources"][0]
        self.assertEqual((src["uid"], src["type"], src["url"]), ("prometheus", "prometheus", "http://127.0.0.1:9090"))
        self.assertFalse(src["editable"])
        dash = yaml.safe_load(read(os.path.join(GRAFANA, "provisioning", "dashboards", "forge.yml")))
        self.assertEqual(dash["providers"][0]["options"]["path"], "/var/lib/grafana/dashboards")
        board = json.loads(read(os.path.join(GRAFANA, "forge-overview.json")))
        self.assertEqual(board.get("uid"), "forge-overview")


class DeployMonitorVerb(unittest.TestCase):
    def setUp(self):
        self.script = read(DEPLOY)

    def test_verb_wiring(self):
        self.assertIn("monitor) shift;", self.script)
        self.assertIn("cmd_monitor()", self.script)
        self.assertIn('GRAFANA_CONF="$ROOT/infra/monitoring/grafana/apt-repo.conf"', self.script)
        # The key is verified against the pinned fingerprint before apt trusts it.
        self.assertIn("does not match the pinned", self.script)
        self.assertIn("/etc/apt/keyrings/grafana.gpg", self.script)
        # Config is checked before it is loaded, locally when possible and always on the host.
        self.assertIn("promtool check config /etc/prometheus/prometheus.yml", self.script)
        # The WireGuard key is the only secret and lands 0600 root.
        self.assertIn("/etc/wireguard/wg0.conf 0600", self.script)
        self.assertIn("wireguard_private_keys.$mon", self.script)
        # Grafana is loopback only; the operator tunnels.
        self.assertIn("ssh -N -L 3000:127.0.0.1:3000", self.script)

    def test_sysupdate_installs_bird_exporter(self):
        self.assertIn("/etc/systemd/system/prometheus-bird-exporter.service 0644", self.script)
        self.assertIn("apt-get install -qq -y prometheus-bird-exporter", self.script)
        self.assertIn("systemctl enable --now prometheus-bird-exporter", self.script)


if __name__ == "__main__":
    sys.exit(unittest.main())
