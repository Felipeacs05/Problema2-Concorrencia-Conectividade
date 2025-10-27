#!/bin/bash

echo "=== Teste de Correções Cross-Server ==="
echo "Este script testa as correções implementadas para comunicação entre servidores diferentes"
echo ""

# Função para testar se um servidor está respondendo
test_server() {
    local server=$1
    local port=$2
    echo "Testando servidor $server:$port..."
    
    if curl -s --connect-timeout 5 "http://$server:$port/servers" > /dev/null; then
        echo "✓ Servidor $server:$port está respondendo"
        return 0
    else
        echo "✗ Servidor $server:$port não está respondendo"
        return 1
    fi
}

# Função para testar comunicação entre servidores
test_cross_server_communication() {
    echo "Testando comunicação entre servidores..."
    
    # Testa se servidor1 pode se comunicar com servidor2
    echo "Testando comunicação servidor1 -> servidor2..."
    if curl -s --connect-timeout 10 "http://servidor1:8080/servers" | grep -q "servidor2"; then
        echo "✓ Servidor1 pode ver servidor2"
    else
        echo "✗ Servidor1 não pode ver servidor2"
    fi
    
    # Testa se servidor2 pode se comunicar com servidor1
    echo "Testando comunicação servidor2 -> servidor1..."
    if curl -s --connect-timeout 10 "http://servidor2:8080/servers" | grep -q "servidor1"; then
        echo "✓ Servidor2 pode ver servidor1"
    else
        echo "✗ Servidor2 não pode ver servidor1"
    fi
}

# Função para testar matchmaking cross-server
test_cross_server_matchmaking() {
    echo "Testando matchmaking cross-server..."
    
    # Simula uma requisição de matchmaking
    echo "Simulando requisição de matchmaking..."
    response=$(curl -s --connect-timeout 10 -X POST "http://servidor1:8080/matchmaking/solicitar_oponente" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $(echo 'servidor1' | base64)" \
        -d '{"solicitante_id":"test123","solicitante_nome":"TestPlayer","servidor_origem":"servidor1:8080"}')
    
    if echo "$response" | grep -q "partida_encontrada"; then
        echo "✓ Matchmaking cross-server funcionando"
    else
        echo "✗ Matchmaking cross-server com problemas"
    fi
}

# Função para testar chat cross-server
test_cross_server_chat() {
    echo "Testando chat cross-server..."
    
    # Simula uma mensagem de chat
    echo "Simulando mensagem de chat..."
    response=$(curl -s --connect-timeout 10 -X POST "http://servidor1:8080/game/chat" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $(echo 'servidor1' | base64)" \
        -d '{"sala_id":"test123","nome_jogador":"TestPlayer","texto":"Teste de chat"}')
    
    if echo "$response" | grep -q "chat_relayed"; then
        echo "✓ Chat cross-server funcionando"
    else
        echo "✗ Chat cross-server com problemas"
    fi
}

# Executa os testes
echo "Iniciando testes..."
echo ""

# Testa se os servidores estão respondendo
echo "1. Testando disponibilidade dos servidores..."
test_server "servidor1" "8080"
test_server "servidor2" "8080"
test_server "servidor3" "8080"
echo ""

# Testa comunicação entre servidores
echo "2. Testando comunicação entre servidores..."
test_cross_server_communication
echo ""

# Testa matchmaking cross-server
echo "3. Testando matchmaking cross-server..."
test_cross_server_matchmaking
echo ""

# Testa chat cross-server
echo "4. Testando chat cross-server..."
test_cross_server_chat
echo ""

echo "=== Teste Concluído ==="
echo "Verifique os logs dos servidores para mais detalhes sobre a comunicação cross-server"


