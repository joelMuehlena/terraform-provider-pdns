# PostgreSQL image of the gpgsql backend for the acceptance tests, read by
# test/acc/run.sh. Not built: the file only pins the version so that
# Dependabot (docker ecosystem) proposes updates.
FROM docker.io/library/postgres:17.12-alpine
