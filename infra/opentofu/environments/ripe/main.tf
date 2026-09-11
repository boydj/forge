# RIPE Database objects for AS215520, read from the RPSL templates in
# infra/network/irr/ so that the human-readable files stay the single
# source of truth. Lines starting with '#' are comments; continuation lines
# are not used in these templates.
locals {
  irr_dir = "${path.module}/../../../network/irr"

  rpsl = {
    as_set  = file("${local.irr_dir}/as-set-AS215520-AS-ALL.rpsl")
    aut_num = file("${local.irr_dir}/aut-num-AS215520.rpsl")
  }

  attrs = {
    for name, text in local.rpsl : name => [
      for line in split("\n", text) :
      {
        name  = regex("^([a-z0-9-]+):\\s*(.*)$", line)[0]
        value = trimspace(regex("^([a-z0-9-]+):\\s*(.*)$", line)[1])
      }
      if trimspace(line) != "" && !startswith(line, "#")
    ]
  }
}

# Create the as-set first: the aut-num export lines reference it.
resource "ripedb_object" "as_set" {
  class      = local.attrs.as_set[0].name
  value      = local.attrs.as_set[0].value
  attributes = local.attrs.as_set
}

resource "ripedb_object" "aut_num" {
  class      = local.attrs.aut_num[0].name
  value      = local.attrs.aut_num[0].value
  attributes = local.attrs.aut_num
  # RIPE NCC-managed attributes (sponsoring-org, status, mnt-by
  # RIPE-NCC-END-MNT, created, last-modified) are submitted verbatim; the
  # local validator does not know all of them.
  ignore_unknown_keys = true

  depends_on = [ripedb_object.as_set]
}

output "as_set" { value = "${local.attrs.as_set[0].name}:${local.attrs.as_set[0].value}" }
output "aut_num" { value = "${local.attrs.aut_num[0].name}:${local.attrs.aut_num[0].value}" }
