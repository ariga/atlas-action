schema "public" {}

table "t1" {
  schema = schema.public
  column "c1" {
      type = int
  }
  index "t1-c1-idx" {
    columns = [ column.c1 ]
  }
}