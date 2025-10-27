#!/bin/bash

# Script para testar a correção do deadlock em partidas cross-server

echo "=========================================="
echo "TESTE: Correção de Deadlock Cross-Server"
echo "=========================================="
echo ""

# Limpa containers antigos
echo "[1/4] Limpando ambiente..."
docker-compose down -v 2>/dev/null
docker system prune -f >/dev/null 2>&1

# Reconstrói as imagens
echo "[2/4] Reconstruindo imagens..."
docker-compose build --no-cache >/dev/null 2>&1

# Inicia os servidores
echo "[3/4] Iniciando servidores..."
docker-compose up -d broker1 broker2 broker3 servidor1 servidor2 servidor3
sleep 10

echo "[4/4] Servidores prontos. Aguardando eleição de líder..."
sleep 5

echo ""
echo "=========================================="
echo "INSTRUÇÕES PARA TESTE MANUAL:"
echo "=========================================="
echo ""
echo "1. Abra DOIS TERMINAIS separados"
echo ""
echo "2. No TERMINAL 1, execute:"
echo "   docker-compose run --rm cliente"
echo "   - Digite um nome (ex: Felipe)"
echo "   - Escolha servidor 1"
echo "   - Aguarde a partida"
echo ""
echo "3. No TERMINAL 2, execute:"
echo "   docker-compose run --rm cliente"  
echo "   - Digite um nome (ex: Davi)"
echo "   - Escolha servidor 2"
echo "   - Aguarde encontrar oponente"
echo ""
echo "4. Quando a partida for encontrada:"
echo "   - AMBOS devem digitar: /comprar"
echo "   - Observe se o chat continua funcionando"
echo "   - Verifique se a partida INICIA corretamente"
echo ""
echo "5. ESPERADO:"
echo "   - Ambos compram cartas"
echo "   - O chat deve continuar funcionando"
echo "   - A partida deve INICIAR automaticamente"
echo "   - Deve aparecer: 'Partida iniciada! É a vez de...'"
echo ""
echo "=========================================="
echo "LOGS DOS SERVIDORES:"
echo "=========================================="
echo "Para acompanhar os logs detalhados:"
echo "  docker-compose logs -f servidor1 servidor2"
echo ""
echo "Para parar o teste:"
echo "  docker-compose down"
echo ""


