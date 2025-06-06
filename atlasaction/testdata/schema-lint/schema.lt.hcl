schema "public" {}

table "t1" {
  schema = schema.public
  column "c1" {
      type = int
  }
}

table "t2" {
  schema = schema.public
  column "c1" {
      type = int
  }
}

