"""Validate the cloud-init template and the files it embeds without OpenTofu.

`infra/cloud-init/node.yaml.tftpl` is rendered by `templatefile` in
modules/forge-node. This test substitutes every `${name}` and
`${indent(N, name)}` placeholder with dummy values (multi-line for the
embedded files, indented the way the template expects) and checks that:

* the result parses as YAML and starts with `#cloud-config`;
* the embedded systemd units, nftables ruleset, forge.toml and backup
  script survive the YAML block scalars byte-for-byte;
* the inline copies of forge-secrets.service and forge-deploy-helper (which
  modules/forge-node cannot pass as variables) match infra/systemd/;
* the deploy user's sudo is scoped to the helper (SR-19a) and secrets are
  read from /run/forge, never /etc/forge/secrets (SR-02);
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
SYSTEMD = os.path.join(ROOT, "infra", "systemd")
HELPER = os.path.join(SYSTEMD, "forge-deploy-helper")
SECRETS_UNIT = os.path.join(SYSTEMD, "forge-secrets.service")
DEPLOY = os.path.join(ROOT, "scripts", "deploy")
# 250 lines of template plus the two inline files (helper ~165, unit ~40).
MAX_LINES = 480
SUDO_RULE = "ALL=(root) NOPASSWD: /usr/local/sbin/forge-deploy-helper *"

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
            "announcer": "",
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
            "forge_bgp_service": read(os.path.join(ROOT, "infra", "systemd", "forge-bgp.service")),
            "forge_bgp_path": read(os.path.join(ROOT, "infra", "systemd", "forge-bgp.path")),
            "forge_bgp_request": read(os.path.join(ROOT, "infra", "systemd", "forge-bgp-request")),
            "forge_bgp_exec": read(os.path.join(ROOT, "infra", "systemd", "forge-bgp-exec")),
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
        self.assertEqual(deploy["sudo"], [SUDO_RULE], "deploy may sudo only the helper (SR-19a)")
        self.assertNotIn("NOPASSWD:ALL", self.rendered)
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
            "/etc/systemd/system/forge-bgp.service": "forge_bgp_service",
            "/etc/systemd/system/forge-bgp.path": "forge_bgp_path",
            "/usr/local/bin/forge-bgp-request": "forge_bgp_request",
            "/usr/local/sbin/forge-bgp-exec": "forge_bgp_exec",
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
        # Inline copies (no template variable exists for them): byte-for-byte with infra/systemd.
        self.assertEqual(files["/usr/local/sbin/forge-deploy-helper"]["content"].rstrip("\n"), read(HELPER).rstrip("\n"))
        self.assertEqual(files["/usr/local/sbin/forge-deploy-helper"]["permissions"], "0755")
        self.assertEqual(files["/etc/systemd/system/forge-secrets.service"]["content"].rstrip("\n"), read(SECRETS_UNIT).rstrip("\n"))

    @unittest.skipIf(yaml is None, "PyYAML not installed")
    def test_runcmd_secrets_and_helper(self):
        doc = yaml.safe_load(self.rendered)
        joined = "\n".join(" ".join(map(str, c)) for c in doc["runcmd"])
        self.assertIn("systemctl enable --now forge-secrets.service", joined)
        self.assertIn("/usr/local/sbin/forge-deploy-helper init-node", joined)
        self.assertNotIn("/etc/forge/secrets", joined, "no plaintext secrets directory on the root filesystem (SR-02)")
        self.assertIn("ssh_host_ed25519_key.pub", joined, "host key must be printed to the console for pinning (SR-19b)")

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

    @unittest.skipIf(tomllib is None, "tomllib needs Python 3.11+")
    def test_secrets_in_tmpfs(self):
        cfg = tomllib.loads(render_toml())
        for path in (cfg["gemini"]["cert_file"], cfg["gemini"]["key_file"], cfg["ssh"]["host_key_file"], cfg["cluster"]["secret_file"]):
            self.assertTrue(path.startswith("/run/forge/secrets/"), path)
        self.assertNotIn("/etc/forge/secrets", render_toml())


class SecretsUnits(unittest.TestCase):
    """forge-secrets.service, forge.service and the helper agree on the tmpfs layout (SR-02)."""

    def test_forge_requires_secrets_unit(self):
        unit = read(os.path.join(SYSTEMD, "forge.service"))
        self.assertIn("Requires=forge-secrets.service", unit)
        self.assertIn("After=forge-secrets.service", unit)
        # /run/forge belongs to forge-secrets.service; a second RuntimeDirectory=forge would chown it to forge.
        self.assertNotRegex(unit, r"(?m)^RuntimeDirectory=")
        self.assertNotIn("/etc/forge/secrets", unit)

    def test_secrets_unit(self):
        unit = read(SECRETS_UNIT)
        for needle in ("Type=oneshot", "RemainAfterExit=yes", "RuntimeDirectory=forge", "RuntimeDirectoryPreserve=yes",
                       "ExecStart=/usr/local/sbin/forge-deploy-helper load-secrets", "WantedBy=multi-user.target"):
            self.assertIn(needle, unit)
        before = re.search(r"(?m)^Before=(.*)$", unit).group(1).split()
        for dep in ("forge.service", "bird.service", "wg-quick@wg0.service"):
            self.assertIn(dep, before)

    def test_backup_reads_recipient_from_tmpfs(self):
        unit = read(os.path.join(SYSTEMD, "forge-backup.service"))
        self.assertIn("Environment=RECIPIENT_FILE=/run/forge/secrets/backup.recipient", unit)

    def test_units_and_helper_use_no_template_directives(self):
        # Both are pasted into node.yaml.tftpl verbatim: templatefile would choke on ${ or %{.
        for path in (HELPER, SECRETS_UNIT):
            text = read(path)
            self.assertNotIn("${", text, path)
            self.assertNotIn("%{", text, path)


class DeployHelper(unittest.TestCase):
    """The deploy user's only root command: small, strict, data-only (SR-19a)."""

    def setUp(self):
        self.helper = read(HELPER)

    def test_shape(self):
        self.assertTrue(self.helper.startswith("#!/bin/bash\n"))
        self.assertIn("set -euo pipefail", self.helper)
        self.assertIn("umask 077", self.helper)
        self.assertIn('[ "$(id -u)" = 0 ] || die', self.helper)

    def test_allowlist_is_data_only(self):
        block = self.helper[self.helper.index("spec() {"):self.helper.index("check() {")]
        names = re.findall(r"^\s+([A-Za-z0-9.]+)\)\s+echo\s+(\S+)", block, re.M)
        self.assertEqual(
            sorted(n for n, _ in names),
            ["bird.conf", "forge.toml", "nftables.conf", "secrets", "state.conf", "wg0.conf"],
        )
        for _, path in names:
            self.assertFalse(path.startswith(("/usr/local/bin", "/usr/local/sbin", "/etc/systemd", "/etc/sudoers")), path)
        # what wg-quick would run as root is checked
        self.assertIn("(Pre|Post)(Up|Down)", self.helper)

    def test_tmpfs_paths(self):
        self.assertIn("RUN=/run/forge", self.helper)
        self.assertIn("BUNDLE=/etc/forge/secrets.tar.age", self.helper)
        self.assertIn("rm -rf /etc/forge/secrets", self.helper)
        for link in ("/etc/wireguard/wg0.conf", "/etc/bird/bird.conf"):
            self.assertIn(link, self.helper)

    def test_bash_syntax(self):
        import subprocess
        for path in (HELPER, DEPLOY):
            subprocess.run(["bash", "-n", path], check=True)


class DeployScript(unittest.TestCase):
    def test_strict_host_keys_and_helper_only(self):
        s = read(DEPLOY)
        self.assertIn("StrictHostKeyChecking=yes", s)
        self.assertNotIn("accept-new", s)
        self.assertIn('KNOWN_HOSTS="$ROOT/infra/known_hosts"', s)
        self.assertNotIn("deploy ALL=(ALL) NOPASSWD:ALL", s)  # only the sysupdate cleanup sed may mention it
        self.assertNotIn("/etc/forge/secrets/", s)
        # the deploy user never runs anything as root other than the helper
        self.assertNotIn("sudo -n sh", s)


class NftablesTemplate(unittest.TestCase):
    def test_renders_and_mentions_ports(self):
        out = render_nftables()
        self.assertNotIn("${", out)
        for needle in ("policy drop", "ADMIN_SSH  = 2200", "{ 22, 1965 }", "udp dport $WG_PORT", "iifname $WG_IF tcp dport $PRIVATE_TCP"):
            self.assertIn(needle, out)


if __name__ == "__main__":
    sys.exit(unittest.main())
