"""Validate the cloud-init template and the files it embeds without OpenTofu.

`infra/cloud-init/node.yaml.tftpl` is rendered by `templatefile` in
modules/forge-node. This test substitutes every `${name}` and
`${indent(N, name)}` placeholder with dummy values (multi-line for the
embedded files, indented the way the template expects) and checks that:

* the result parses as YAML and starts with `#cloud-config`;
* the embedded systemd units, nftables ruleset, forge.toml and backup
  script survive the YAML block scalars byte-for-byte;
* the template stays under the agreed size and uses no `%{ }` directives
  (which this test cannot emulate);
* forge.toml.tftpl renders to valid TOML.

Run: python3 -m unittest discover -s tests/infra
"""
import os
import re
import sys
import unittest

try:
    import yaml
except ImportError:  # pragma: no cover
    yaml = None

try:
    import tomllib
except ImportError:  # pragma: no cover
    tomllib = None

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
CLOUD_INIT = os.path.join(ROOT, "infra", "cloud-init", "node.yaml.tftpl")
FORGE_TOML = os.path.join(ROOT, "infra", "cloud-init", "forge.toml.tftpl")
NFTABLES = os.path.join(ROOT, "infra", "firewall", "nftables.conf.tftpl")
MAX_LINES = 250

PLACEHOLDER_RE = re.compile(r"\$\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}")
INDENT_RE = re.compile(r"\$\{\s*indent\(\s*(\d+)\s*,\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\)\s*\}")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def render(template, scalars, blocks):
    """Emulate templatefile for the two forms the cloud-init template uses."""

    def indent_sub(m):
        n, name = int(m.group(1)), m.group(2)
        text = blocks[name]
        lines = text.split("\n")
        return lines[0] + "".join("\n" + (" " * n + l if l else "") for l in lines[1:])

    out = INDENT_RE.sub(indent_sub, template)

    def scalar_sub(m):
        name = m.group(1)
        if name in scalars:
            return str(scalars[name])
        if name in blocks:
            return blocks[name]
        raise KeyError(f"no dummy value for placeholder ${{{name}}}")

    return PLACEHOLDER_RE.sub(scalar_sub, out)


def render_toml():
    return render(
        read(FORGE_TOML),
        {
            "node": "ewr1",
            "service_hostname": "git.example.org",
            "title": "forge",
            "gemini_listen": '[":1965"]',
            "ssh_listen": '[":22"]',
            "metrics_listen": "[fd42::1]:9100",
            "cluster_enabled": "true",
            "control_listen": "[fd42::1]:9200",
        },
        {},
    )


def render_nftables():
    return render(
        read(NFTABLES),
        {
            "anycast_v4": "192.0.2.1",
            "anycast_v6": "2001:db8::1",
            "admin_ssh_port": 2200,
            "wg_port": 51820,
            "wg_iface": "wg0",
            "private_tcp_ports": "9100, 9101, 9200",
        },
        {},
    )


class CloudInitTemplate(unittest.TestCase):
    def setUp(self):
        self.template = read(CLOUD_INIT)
        self.blocks = {
            "forge_toml": render_toml(),
            "nftables_conf": render_nftables(),
            "forge_service": read(os.path.join(ROOT, "infra", "systemd", "forge.service")),
            "forge_maintenance_service": read(os.path.join(ROOT, "infra", "systemd", "forge-maintenance.service")),
            "forge_maintenance_timer": read(os.path.join(ROOT, "infra", "systemd", "forge-maintenance.timer")),
            "forge_backup_service": read(os.path.join(ROOT, "infra", "systemd", "forge-backup.service")),
            "forge_backup_timer": read(os.path.join(ROOT, "infra", "systemd", "forge-backup.timer")),
            "forge_backup_script": read(os.path.join(ROOT, "infra", "backup", "forge-backup")),
            "anycast_network_addresses": "Address=192.0.2.1/32\nAddress=2001:db8::1/128",
            "operator_ssh_authorized_keys": "      - ssh-ed25519 AAAAexample operator\n      - ssh-ed25519 AAAAexample2 ci",
        }
        self.scalars = {
            "node": "ewr1",
            "hostname_fqdn": "ewr1.nodes.example.org",
            "service_hostname": "git.example.org",
            "role": "leader",
            "pop_index": 1,
            "admin_ssh_port": 2200,
        }
        self.rendered = render(self.template, self.scalars, self.blocks)

    def test_size_and_directives(self):
        n = self.template.count("\n")
        self.assertLessEqual(n, MAX_LINES, f"node.yaml.tftpl is {n} lines; keep it under {MAX_LINES}")
        self.assertNotIn("%{", self.template, "template directives (%{ }) are not emulated by this test")
        self.assertTrue(self.template.startswith("#cloud-config\n"))

    def test_no_unrendered_placeholders(self):
        # Embedded files (the bash backup script) legitimately contain ${...};
        # check the template itself with every block replaced by a marker.
        flat = render(self.template, self.scalars, {k: "X" for k in self.blocks})
        self.assertNotIn("${", flat)

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_yaml_structure(self):
        doc = yaml.safe_load(self.rendered)
        self.assertIsInstance(doc, dict)
        for key in ("users", "packages", "write_files", "runcmd", "final_message"):
            self.assertIn(key, doc)
        self.assertEqual(doc["hostname"], "ewr1")
        self.assertFalse(doc["ssh_pwauth"])
        names = [u["name"] for u in doc["users"]]
        self.assertEqual(names, ["forge", "deploy"])
        deploy = doc["users"][1]
        self.assertEqual(len(deploy["ssh_authorized_keys"]), 2)
        for pkg in ("git", "bird2", "wireguard-tools", "nftables", "age", "unattended-upgrades", "prometheus-node-exporter"):
            self.assertIn(pkg, doc["packages"])
        for item in doc["runcmd"]:
            self.assertIsInstance(item, list, f"runcmd entries must be argv lists: {item!r}")

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_embedded_files_roundtrip(self):
        doc = yaml.safe_load(self.rendered)
        files = {f["path"]: f for f in doc["write_files"]}
        expect = {
            "/etc/forge/forge.toml": "forge_toml",
            "/etc/nftables.conf": "nftables_conf",
            "/etc/systemd/system/forge.service": "forge_service",
            "/etc/systemd/system/forge-maintenance.service": "forge_maintenance_service",
            "/etc/systemd/system/forge-maintenance.timer": "forge_maintenance_timer",
            "/etc/systemd/system/forge-backup.service": "forge_backup_service",
            "/etc/systemd/system/forge-backup.timer": "forge_backup_timer",
            "/usr/local/bin/forge-backup": "forge_backup_script",
        }
        for path, name in expect.items():
            self.assertIn(path, files)
            self.assertEqual(files[path]["content"].rstrip("\n"), self.blocks[name].rstrip("\n"), path)
        sshd = files["/etc/ssh/sshd_config.d/10-forge.conf"]["content"]
        self.assertIn("Port 2200", sshd)
        self.assertIn("PasswordAuthentication no", sshd)
        self.assertIn("PermitRootLogin prohibit-password", sshd)
        for path in ("/etc/nftables.conf", "/etc/bird/bird.conf", "/etc/default/prometheus-node-exporter"):
            self.assertTrue(files[path].get("defer"), f"{path} is shipped by a package; must use defer: true")
        self.assertEqual(files["/usr/local/bin/forge-backup"]["permissions"], "0755")

    def test_no_downloads_in_runcmd(self):
        doc = yaml.safe_load(self.rendered) if yaml else None
        if doc is None:
            self.skipTest("PyYAML not installed")
        joined = " ".join(" ".join(map(str, c)) for c in doc["runcmd"])
        for bad in ("curl ", "wget ", "github.com", "http://", "https://"):
            self.assertNotIn(bad, joined, "cloud-init must not download binaries; scripts/deploy pushes them")


class ForgeTomlTemplate(unittest.TestCase):
    @unittest.skipIf(tomllib is None, "tomllib needs Python 3.11+")
    def test_valid_toml(self):
        cfg = tomllib.loads(render_toml())
        self.assertEqual(cfg["node"], "ewr1")
        self.assertEqual(cfg["gemini"]["listen"], [":1965"])
        self.assertEqual(cfg["ssh"]["listen"], [":22"])
        self.assertEqual(cfg["ssh"]["port"], 22)
        self.assertTrue(cfg["cluster"]["enabled"])
        self.assertEqual(cfg["metrics"]["listen"], "[fd42::1]:9100")


class NftablesTemplate(unittest.TestCase):
    def test_renders_and_mentions_ports(self):
        out = render_nftables()
        self.assertNotIn("${", out)
        for needle in ("policy drop", "ADMIN_SSH  = 2200", "{ 22, 1965 }", "udp dport WG_PORT", "iifname WG_IF tcp dport PRIVATE_TCP"):
            self.assertIn(needle, out)


if __name__ == "__main__":
    sys.exit(unittest.main())
