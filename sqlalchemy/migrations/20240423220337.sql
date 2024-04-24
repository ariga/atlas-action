-- Create "user_account" table
CREATE TABLE "user_account" (
  "id" serial NOT NULL,
  "name" character varying(30) NOT NULL,
  "fullname" character varying(30) NULL,
  "nickname" character varying(30) NULL,
  PRIMARY KEY ("id")
);
-- Create "address" table
CREATE TABLE "address" (
  "id" serial NOT NULL,
  "email_address" character varying(30) NOT NULL,
  "user_id" integer NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "address_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "user_account" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
