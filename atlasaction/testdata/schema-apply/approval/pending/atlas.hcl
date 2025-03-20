variable "current_timestamp" {
  type = string
  default = getenv("CURRENT_TIMESTAMP")
}

data "hcl_schema" "app" {
  path = "schema.lt.hcl"
  vars = {
    rand = var.current_timestamp
  }
}

env "test" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  schema {
    src = data.hcl_schema.app.url
    repo {
      name = "atlas-action"
    }
  }
}