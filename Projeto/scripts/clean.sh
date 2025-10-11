#!/bin/bash

# Script de limpeza do projeto

echo "========================================="
echo "  Limpeza do Projeto"
echo "========================================="
echo

# Remove binários
echo "[1/4] Removendo binários..."
rm -rf bin/
rm -f servidor/servidor cliente/cliente
echo "✓ Binários removidos"
echo

# Remove arquivos de teste
echo "[2/4] Removendo arquivos de teste..."
rm -f *.test
rm -f coverage.out
echo "✓ Arquivos de teste removidos"
echo

# Remove logs
echo "[3/4] Removendo logs..."
rm -f mosquitto/log/*.log
rm -f *.log
echo "✓ Logs removidos"
echo

# Remove dados temporários
echo "[4/4] Removendo dados temporários..."
rm -rf mosquitto/data/*
rm -rf tmp/
rm -rf temp/
echo "✓ Dados temporários removidos"
echo

echo "========================================="
echo "  Limpeza concluída!"
echo "========================================="

