predicate "index" "not-null" {
  self {
    ne = null
  }
}

predicate "table" "has-primary-key" {
  primary_key {
    predicate = predicate.index.not-null
  }
}

rule "schema" "primary-key-required" {
  description = "All tables must have a primary key"
  table {
    assert {
      predicate = predicate.table.has-primary-key
      message = "Table ${self.name} must have a primary key"
    }
  }
}