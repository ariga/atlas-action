ncc build migrate/apply/index.js -o migrate/apply/dist
ncc build migrate/push/index.js -o migrate/push/dist
ncc build migrate/lint/index.js -o migrate/lint/dist
ncc build migrate/down/index.js -o migrate/down/dist
ncc build migrate/test/index.js -o migrate/test/dist
ncc build schema/test/index.js -o schema/test/dist