schema "main" {}

table "t1" {
  schema = schema.main
  column "c1" {
    type = integer
  }
}

table "t2" {
  schema = schema.main
  column "c1" {
    type = integer
  }
}

table "t3" {
  schema = schema.main
  column "c1" {
    type = integer
  }
}
