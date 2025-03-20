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

table "t4" {
  schema = schema.main
  column "c1" {
    type = integer
  }
  column "c2" {
    type = integer
  }
  column "c3" {
    type = integer
  }
}
