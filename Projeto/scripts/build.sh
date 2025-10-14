#!/bin/bash

# Script de build para o projeto

set -e

echo "========================================="
echo "  Build do Projeto"
echo "========================================="
echo

# Cria diretório de binários
mkdir -p bin

# Build do servidor
echo "[1/2] Compilando servidor..."
cd servidor
go build -ldflags="-s -w" -o ../bin/servidor
cd ..
echo "✓ Servidor compilado: bin/servidor"
echo

# Build do cliente
echo "[2/2] Compilando cliente..."
cd cliente
go build -ldflags="-s -w" -o ../bin/cliente
cd ..
echo "✓ Cliente compilado: bin/cliente"
echo

echo "========================================="
echo "  Build concluído!"
echo "  Binários em: bin/"
echo "========================================="

