schema "main" {}

table "t1" {
  schema = schema.main
  column "c1" {
    type = integer
  }
}
