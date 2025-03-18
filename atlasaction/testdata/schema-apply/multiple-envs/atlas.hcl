env {
  name     = atlas.env
  for_each = ["bu", "pi", "su"]
  url      = format("sqlite://local-%s.db", each.value)
  dev      = "sqlite://file?mode=memory"
  src      = "file://schema.lt.hcl"
}
