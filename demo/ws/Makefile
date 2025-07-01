
.PHONY: e2e_up
e2e_up:
	@docker compose -p ws-gateway -f scripts/test_docker_compose.yml up -d

.PHONY: e2e_down
e2e_down:
	@echo "清理Docker Compose文件定义的容器..."
	@docker compose -p ws-gateway -f scripts/test_docker_compose.yml down -v

.PHONY: mock_backend
mock_backend:
	@cd backend && export EGO_DEBUG=true && nohup go run main.go --config=config/config.yaml &

.PHONY: run_gateway_only
run_gateway_only:
	@cd ../../cmd  &&  GATEWAY_NODE_ID=1 GATEWAY_NODE_LOCATION=beijing GATEWAY_STOP_TIMEOUT=25 && go run main.go --config=../config/config.stress.yaml
