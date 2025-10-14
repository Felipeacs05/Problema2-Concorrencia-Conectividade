#!/bin/bash

# Script de testes para o projeto

set -e

echo "========================================="
echo "  Executando Testes do Projeto"
echo "========================================="
echo

# Testa compilação
echo "[1/4] Testando compilação..."
cd servidor && go build -o ../bin/servidor && cd ..
cd cliente && go build -o ../bin/cliente && cd ..
echo "✓ Compilação bem-sucedida"
echo

# Executa testes unitários
echo "[2/4] Executando testes unitários..."
cd servidor && go test -v -cover && cd ..
echo "✓ Testes unitários passaram"
echo

# Executa benchmarks
echo "[3/4] Executando benchmarks..."
cd servidor && go test -bench=. -benchmem && cd ..
echo "✓ Benchmarks executados"
echo

# Verifica formatação
echo "[4/4] Verificando formatação..."
gofmt -l servidor/*.go cliente/*.go protocolo/*.go
echo "✓ Formatação verificada"
echo

echo "========================================="
echo "  Todos os testes passaram!"
echo "========================================="

