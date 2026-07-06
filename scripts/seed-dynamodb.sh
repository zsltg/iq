#!/usr/bin/env bash
# Seed the local DynamoDB (from compose.yaml) with a small example dataset, so
# `iq` has a table to explore. The data is ephemeral: `docker compose down`
# discards it (the container runs -inMemory). Re-run any time to reset. The books
# table uses an integer partition key, so the jq key mapping (key = the bare
# partition-key value) is obvious.
#
# Needs the AWS CLI. DynamoDB Local ignores credentials, so dummy values are set
# inline; the endpoint points at the compose service on :8000.
set -euo pipefail

export AWS_ACCESS_KEY_ID=dummy
export AWS_SECRET_ACCESS_KEY=dummy
export AWS_DEFAULT_REGION=us-east-1
endpoint=http://localhost:8000

if ! command -v aws >/dev/null 2>&1; then
  echo "The AWS CLI is required to seed DynamoDB Local. Install it: https://aws.amazon.com/cli/" >&2
  exit 1
fi

if ! aws dynamodb list-tables --endpoint-url "$endpoint" >/dev/null 2>&1; then
  echo "DynamoDB Local is not reachable at $endpoint. Start it first: docker compose up -d --wait dynamodb" >&2
  exit 1
fi

aws dynamodb delete-table --endpoint-url "$endpoint" --table-name books >/dev/null 2>&1 || true
aws dynamodb wait table-not-exists --endpoint-url "$endpoint" --table-name books >/dev/null 2>&1 || true

aws dynamodb create-table --endpoint-url "$endpoint" \
  --table-name books \
  --attribute-definitions AttributeName=id,AttributeType=N \
  --key-schema AttributeName=id,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST >/dev/null
aws dynamodb wait table-exists --endpoint-url "$endpoint" --table-name books >/dev/null

put() {
  aws dynamodb put-item --endpoint-url "$endpoint" --table-name books --item "$1" >/dev/null
}
put '{"id":{"N":"1"},"title":{"S":"The Go Programming Language"},"author":{"S":"Donovan and Kernighan"},"year":{"N":"2015"},"price":{"N":"39"},"tags":{"SS":["go","programming"]}}'
put '{"id":{"N":"2"},"title":{"S":"Designing Data-Intensive Applications"},"author":{"S":"Martin Kleppmann"},"year":{"N":"2017"},"price":{"N":"45"},"tags":{"SS":["data","architecture"]}}'
put '{"id":{"N":"3"},"title":{"S":"A Philosophy of Software Design"},"author":{"S":"John Ousterhout"},"year":{"N":"2018"},"price":{"N":"20"},"tags":{"SS":["design"]}}'
put '{"id":{"N":"4"},"title":{"S":"Clean Code"},"author":{"S":"Robert C. Martin"},"year":{"N":"2008"},"price":{"N":"35"},"tags":{"SS":["craft","design"]}}'

count=$(aws dynamodb scan --endpoint-url "$endpoint" --table-name books --select COUNT --query Count --output text)
echo "Seeded example data: ${count} items in books. Try: iq add 'dynamodb://us-east-1/?table=books&endpoint=${endpoint}' -n books && iq --src books '.[\"2\"]'"
