#!/bin/bash

# Script de teste para verificar as correções de comunicação cross-server
# Versão 2 - Com correções mais robustas

echo "=== TESTE DE CORREÇÕES CROSS-SERVER V2 ==="
echo "Testando comunicação entre servidores com correções robustas..."
echo

# Função para testar se um servidor está respondendo
test_server() {
    local server=$1
    echo "Testando servidor $server..."
    
    if curl -s --connect-timeout 5 "http://$server/servers" > /dev/null 2>&1; then
        echo "✅ Servidor $server está respondendo"
        return 0
    else
        echo "❌ Servidor $server não está respondendo"
        return 1
    fi
}

# Função para testar comunicação cross-server
test_cross_server_communication() {
    echo "Testando comunicação cross-server..."
    
    # Testar se servidor1 pode ver servidor2
    echo "Testando servidor1 -> servidor2..."
    response=$(curl -s "http://servidor1:8080/servers" 2>/dev/null)
    if echo "$response" | grep -q "servidor2:8080"; then
        echo "✅ Servidor1 pode ver servidor2"
    else
        echo "❌ Servidor1 não pode ver servidor2"
    fi
    
    # Testar se servidor2 pode ver servidor1
    echo "Testando servidor2 -> servidor1..."
    response=$(curl -s "http://servidor2:8080/servers" 2>/dev/null)
    if echo "$response" | grep -q "servidor1:8080"; then
        echo "✅ Servidor2 pode ver servidor1"
    else
        echo "❌ Servidor2 não pode ver servidor1"
    fi
}

# Função para testar matchmaking cross-server
test_cross_server_matchmaking() {
    echo "Testando matchmaking cross-server..."
    
    # Simular solicitação de matchmaking
    echo "Enviando solicitação de matchmaking..."
    response=$(curl -s -X POST "http://servidor1:8080/matchmaking/solicitar_oponente" \
        -H "Content-Type: application/json" \
        -d '{"nome": "teste1", "servidor": "servidor1:8080"}' 2>/dev/null)
    
    if echo "$response" | grep -q "oponente"; then
        echo "✅ Matchmaking cross-server funcionando"
    else
        echo "❌ Matchmaking cross-server com problemas"
    fi
}

# Função para testar chat cross-server
test_cross_server_chat() {
    echo "Testando chat cross-server..."
    
    # Simular envio de chat
    echo "Enviando mensagem de chat..."
    response=$(curl -s -X POST "http://servidor1:8080/game/chat" \
        -H "Content-Type: application/json" \
        -d '{"sala_id": "teste", "cliente_id": "teste", "texto": "teste"}' 2>/dev/null)
    
    if echo "$response" | grep -q "ok\|success"; then
        echo "✅ Chat cross-server funcionando"
    else
        echo "❌ Chat cross-server com problemas"
    fi
}

# Função para testar sincronização de estado
test_state_sync() {
    echo "Testando sincronização de estado..."
    
    # Simular sincronização de estado
    echo "Enviando estado para sincronização..."
    response=$(curl -s -X POST "http://servidor1:8080/partida/sincronizar_estado" \
        -H "Content-Type: application/json" \
        -d '{"sala_id": "teste", "estado": {"teste": "valor"}}' 2>/dev/null)
    
    if echo "$response" | grep -q "ok\|success"; then
        echo "✅ Sincronização de estado funcionando"
    else
        echo "❌ Sincronização de estado com problemas"
    fi
}

# Função para monitorar logs em tempo real
monitor_logs() {
    echo "Monitorando logs dos servidores..."
    echo "Pressione Ctrl+C para parar o monitoramento"
    echo
    
    # Monitorar logs do docker-compose
    docker-compose logs -f --tail=50 servidor1 servidor2 servidor3
}

# Função principal
main() {
    echo "Iniciando testes de correções cross-server..."
    echo
    
    # Testar servidores
    echo "1. Testando disponibilidade dos servidores..."
    test_server "servidor1:8080"
    test_server "servidor2:8080"
    test_server "servidor3:8080"
    echo
    
    # Testar comunicação
    echo "2. Testando comunicação cross-server..."
    test_cross_server_communication
    echo
    
    # Testar matchmaking
    echo "3. Testando matchmaking cross-server..."
    test_cross_server_matchmaking
    echo
    
    # Testar chat
    echo "4. Testando chat cross-server..."
    test_cross_server_chat
    echo
    
    # Testar sincronização
    echo "5. Testando sincronização de estado..."
    test_state_sync
    echo
    
    echo "=== TESTES CONCLUÍDOS ==="
    echo
    echo "Para monitorar logs em tempo real, execute:"
    echo "docker-compose logs -f servidor1 servidor2 servidor3"
    echo
    echo "Para testar manualmente:"
    echo "1. Execute o cliente em servidor1"
    echo "2. Execute o cliente em servidor2"
    echo "3. Teste chat e compra de cartas"
    echo "4. Verifique se ambos os jogadores podem jogar"
}

# Executar função principal
main "$@"

