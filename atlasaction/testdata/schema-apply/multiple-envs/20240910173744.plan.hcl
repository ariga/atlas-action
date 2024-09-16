plan "20240910173744" {
  from      = "iHZMQ1EoarAXt/KU0KQbBljbbGs8gVqX2ZBXefePSGE="
  to        = "Cp8xCVYilZuwULkggsfJLqIQHaxYcg/IpU+kgjVUBA4="
  migration = <<-SQL
  -- Add column "c2" to table: "t4"
  ALTER TABLE `t4` ADD COLUMN `c2` integer NOT NULL;
  SQL
}
