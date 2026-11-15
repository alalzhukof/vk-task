.PHONY: up stop test browser firewall broker database rejoin backup
up:
	docker compose up -d --build --wait
stop:
	docker compose stop
test:
	docker compose --profile test run --build --rm unit
	docker compose --profile test run --build --rm tests
firewall:
	docker compose --profile tools run --build --rm tools firewall
broker:
	docker compose --profile tools run --build --rm tools rabbit-failover
database:
	docker compose --profile tools run --build --rm tools pg-failover
rejoin:
	docker compose --profile tools run --build --rm tools rejoin
backup:
	docker compose --profile tools run --build --rm tools backup
browser:
	docker compose --profile test run --build --rm browser
