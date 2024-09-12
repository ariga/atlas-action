env {
  name     = atlas.env
  for_each = ["bu", "pi", "su", "bleh"]
  url      = format("sqlite://local-%s.db", each.value)
  dev      = "sqlite://file?mode=memory"
  src      = "file://schema.lt.hcl"
}
