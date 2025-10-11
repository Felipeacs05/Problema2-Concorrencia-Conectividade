.PHONY: help build up down logs clean restart status test

# Variáveis
DOCKER_COMPOSE = docker-compose
GO = go

# Cores para output
COLOR_RESET = \033[0m
COLOR_BOLD = \033[1m
COLOR_GREEN = \033[32m
COLOR_YELLOW = \033[33m
COLOR_BLUE = \033[34m

help: ## Mostra esta ajuda
	@echo "$(COLOR_BOLD)Comandos disponíveis:$(COLOR_RESET)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(COLOR_GREEN)%-15s$(COLOR_RESET) %s\n", $$1, $$2}'

build: ## Constrói as imagens Docker
	@echo "$(COLOR_BLUE)Construindo imagens Docker...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) build

up: ## Inicia todos os serviços
	@echo "$(COLOR_BLUE)Iniciando sistema distribuído...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) up -d
	@echo "$(COLOR_GREEN)Sistema iniciado com sucesso!$(COLOR_RESET)"
	@echo "$(COLOR_YELLOW)Use 'make logs' para ver os logs$(COLOR_RESET)"

up-build: ## Constrói e inicia todos os serviços
	@echo "$(COLOR_BLUE)Construindo e iniciando sistema...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) up --build -d
	@echo "$(COLOR_GREEN)Sistema iniciado com sucesso!$(COLOR_RESET)"

down: ## Para todos os serviços
	@echo "$(COLOR_BLUE)Parando sistema...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) down
	@echo "$(COLOR_GREEN)Sistema parado$(COLOR_RESET)"

down-clean: ## Para todos os serviços e remove volumes
	@echo "$(COLOR_BLUE)Parando sistema e limpando volumes...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) down -v
	@echo "$(COLOR_GREEN)Sistema parado e volumes removidos$(COLOR_RESET)"

logs: ## Mostra logs de todos os servidores
	$(DOCKER_COMPOSE) logs -f servidor1 servidor2 servidor3

logs-all: ## Mostra logs de todos os serviços
	$(DOCKER_COMPOSE) logs -f

logs-s1: ## Mostra logs do servidor 1
	$(DOCKER_COMPOSE) logs -f servidor1

logs-s2: ## Mostra logs do servidor 2
	$(DOCKER_COMPOSE) logs -f servidor2

logs-s3: ## Mostra logs do servidor 3
	$(DOCKER_COMPOSE) logs -f servidor3

logs-brokers: ## Mostra logs de todos os brokers
	$(DOCKER_COMPOSE) logs -f broker1 broker2 broker3

restart: ## Reinicia todos os serviços
	@echo "$(COLOR_BLUE)Reiniciando sistema...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) restart
	@echo "$(COLOR_GREEN)Sistema reiniciado$(COLOR_RESET)"

restart-s1: ## Reinicia servidor 1
	$(DOCKER_COMPOSE) restart servidor1

restart-s2: ## Reinicia servidor 2
	$(DOCKER_COMPOSE) restart servidor2

restart-s3: ## Reinicia servidor 3
	$(DOCKER_COMPOSE) restart servidor3

status: ## Mostra status dos serviços
	@echo "$(COLOR_BOLD)Status dos Containers:$(COLOR_RESET)"
	@$(DOCKER_COMPOSE) ps
	@echo ""
	@echo "$(COLOR_BOLD)Status do Cluster:$(COLOR_RESET)"
	@curl -s http://localhost:8080/servers | jq '.' || echo "Servidor 1 não responde"
	@echo ""
	@echo "$(COLOR_BOLD)Status do Estoque:$(COLOR_RESET)"
	@echo "Servidor 1:"
	@curl -s http://localhost:8080/estoque/status | jq '.' || echo "  Não responde"
	@echo "Servidor 2:"
	@curl -s http://localhost:8081/estoque/status | jq '.' || echo "  Não responde"
	@echo "Servidor 3:"
	@curl -s http://localhost:8082/estoque/status | jq '.' || echo "  Não responde"

cliente: ## Executa um cliente interativo em container
	@echo "$(COLOR_GREEN)Iniciando cliente...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) run --rm cliente

cliente-local: ## Executa cliente localmente (requer Go)
	@echo "$(COLOR_GREEN)Iniciando cliente local...$(COLOR_RESET)"
	cd cliente && $(GO) run main.go

test-deps: ## Baixa dependências Go
	@echo "$(COLOR_BLUE)Baixando dependências...$(COLOR_RESET)"
	$(GO) mod download
	@echo "$(COLOR_GREEN)Dependências baixadas$(COLOR_RESET)"

test-build: ## Testa compilação local
	@echo "$(COLOR_BLUE)Testando compilação do servidor...$(COLOR_RESET)"
	cd servidor && $(GO) build -o ../bin/servidor
	@echo "$(COLOR_BLUE)Testando compilação do cliente...$(COLOR_RESET)"
	cd cliente && $(GO) build -o ../bin/cliente
	@echo "$(COLOR_GREEN)Compilação bem-sucedida$(COLOR_RESET)"

test-unit: ## Executa testes unitários
	@echo "$(COLOR_BLUE)Executando testes unitários...$(COLOR_RESET)"
	cd servidor && $(GO) test -v -cover
	@echo "$(COLOR_GREEN)Testes concluídos$(COLOR_RESET)"

test-bench: ## Executa benchmarks
	@echo "$(COLOR_BLUE)Executando benchmarks...$(COLOR_RESET)"
	cd servidor && $(GO) test -bench=. -benchmem
	@echo "$(COLOR_GREEN)Benchmarks concluídos$(COLOR_RESET)"

test-lider: ## Testa eleição de líder (para servidor 1)
	@echo "$(COLOR_YELLOW)Parando servidor 1 (líder atual)...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) stop servidor1
	@echo "Aguardando nova eleição (15 segundos)..."
	@sleep 15
	@echo "$(COLOR_BLUE)Verificando novo líder:$(COLOR_RESET)"
	@curl -s http://localhost:8081/estoque/status | jq '.lider' || echo "Erro"
	@curl -s http://localhost:8082/estoque/status | jq '.lider' || echo "Erro"
	@echo "$(COLOR_YELLOW)Reiniciando servidor 1...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) start servidor1

clean: ## Remove containers, volumes, e binários
	@echo "$(COLOR_BLUE)Limpando sistema...$(COLOR_RESET)"
	$(DOCKER_COMPOSE) down -v
	rm -rf bin/
	rm -rf mosquitto/data/*
	rm -rf mosquitto/log/*
	@echo "$(COLOR_GREEN)Sistema limpo$(COLOR_RESET)"

ps: ## Lista containers em execução
	$(DOCKER_COMPOSE) ps

shell-s1: ## Abre shell no servidor 1
	$(DOCKER_COMPOSE) exec servidor1 /bin/sh

shell-s2: ## Abre shell no servidor 2
	$(DOCKER_COMPOSE) exec servidor2 /bin/sh

shell-s3: ## Abre shell no servidor 3
	$(DOCKER_COMPOSE) exec servidor3 /bin/sh

# Atalhos
start: up ## Alias para 'up'
stop: down ## Alias para 'down'
l: logs ## Alias para 'logs'
s: status ## Alias para 'status'

.DEFAULT_GOAL := help

