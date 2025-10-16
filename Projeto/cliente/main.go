package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"jogodistribuido/protocolo"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

var (
	meuNome       string
	meuID         string
	mqttClient    mqtt.Client
	salaAtual     string
	oponenteID    string
	oponenteNome  string
	meuInventario []protocolo.Carta
)

func main() {
	fmt.Println("=== Jogo de Cartas Multiplayer Distribuído ===")
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Digite seu nome: ")
	scanner.Scan()
	meuNome = strings.TrimSpace(scanner.Text())
	if meuNome == "" {
		meuNome = "Jogador"
	}

	// --- LÓGICA DE ESCOLHA CORRIGIDA ---
	serverMap := map[int]string{
		1: "tcp://broker1:1883",
		2: "tcp://broker2:1883",
		3: "tcp://broker3:1883",
	}
	fmt.Println("\nEscolha o servidor para conectar:")
	fmt.Println("1. Servidor 1")
	fmt.Println("2. Servidor 2")
	fmt.Println("3. Servidor 3") //asdaddasdsd
	fmt.Print("Opção: ")
	scanner.Scan()
	opcaoStr := scanner.Text()
	opcao, err := strconv.Atoi(opcaoStr)
	if err != nil || serverMap[opcao] == "" {
		log.Fatalf("Opção inválida.")
	}
	brokerAddr := serverMap[opcao]
	// --- FIM DA CORREÇÃO ---

	fmt.Printf("\nConectando ao broker MQTT: %s\n", brokerAddr)

	if err := conectarMQTT(brokerAddr); err != nil {
		log.Fatalf("Erro ao conectar ao MQTT: %v", err)
	}

	// --- LÓGICA DE LOGIN CORRIGIDA ---
	if err := fazerLogin(); err != nil {
		log.Fatalf("Erro no processo de login: %v", err)
	}
	// --- FIM DA CORREÇÃO ---

	fmt.Printf("\nBem-vindo, %s! (Seu ID: %s)\n", meuNome, meuID)
	fmt.Println("\nEntrando na fila de matchmaking...")
	entrarNaFila()

	mostrarAjuda()

	for scanner.Scan() {
		entrada := strings.TrimSpace(scanner.Text())
		processarComando(entrada)
		fmt.Print("> ")
	}
}

func conectarMQTT(broker string) error {
	opts := mqtt.NewClientOptions()
	// --- CORREÇÃO APLICADA AQUI ---
	// Adiciona apenas o broker que o utilizador escolheu.
	opts.AddBroker(broker)
	// --- FIM DA CORREÇÃO ---
	opts.SetClientID("cliente_" + time.Now().Format("20060102150405"))
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetMaxReconnectInterval(10 * time.Second)
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		fmt.Printf("\n[AVISO] Conexão MQTT perdida: %v. Tentando reconectar...\n", err)
	})
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		fmt.Println("\n[INFO] Conectado ao broker MQTT.")
		if meuID != "" {
			// Reinscreve nos tópicos importantes após reconexão
			topicoEventos := fmt.Sprintf("clientes/%s/eventos", meuID)
			client.Subscribe(topicoEventos, 0, handleMensagemServidor)
			if salaAtual != "" {
				topicoPartida := fmt.Sprintf("partidas/%s/eventos", salaAtual)
				client.Subscribe(topicoPartida, 0, handleEventoPartida)
			}
		}
	})

	mqttClient = mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	return nil
}

func fazerLogin() error {
	// Cria um canal para esperar a resposta do login
	loginResponseChan := make(chan protocolo.Mensagem)

	// Gera um ID temporário único para esta sessão de login
	tempID := uuid.New().String()
	responseTopic := fmt.Sprintf("clientes/%s/eventos", tempID)

	// Inscreve-se no tópico de resposta ANTES de enviar o pedido
	if token := mqttClient.Subscribe(responseTopic, 1, func(c mqtt.Client, m mqtt.Message) {
		var msg protocolo.Mensagem
		if err := json.Unmarshal(m.Payload(), &msg); err == nil {
			loginResponseChan <- msg
		}
	}); token.Wait() && token.Error() != nil {
		return fmt.Errorf("falha ao se inscrever no tópico de resposta: %v", token.Error())
	}

	// Publica a mensagem de login num tópico que o servidor ouve
	dadosLogin := protocolo.DadosLogin{Nome: meuNome}
	msgLogin := protocolo.Mensagem{Comando: "LOGIN", Dados: mustJSON(dadosLogin)}
	payloadLogin, _ := json.Marshal(msgLogin)
	// O tópico de login agora inclui o ID temporário para o servidor saber para onde responder
	loginTopic := fmt.Sprintf("clientes/%s/login", tempID)
	mqttClient.Publish(loginTopic, 1, false, payloadLogin)

	// Aguarda a resposta por um tempo limitado (sem time.Sleep!)
	select {
	case resp := <-loginResponseChan:
		if resp.Comando == "LOGIN_OK" {
			var dados map[string]string
			json.Unmarshal(resp.Dados, &dados)
			meuID = dados["cliente_id"] // Guarda o ID permanente recebido do servidor
			servidor := dados["servidor"]
			fmt.Printf("\n[LOGIN] Conectado ao servidor %s\n", servidor)

			// Limpa a inscrição temporária e inscreve-se na permanente
			mqttClient.Unsubscribe(responseTopic)
			permanentTopic := fmt.Sprintf("clientes/%s/eventos", meuID)
			mqttClient.Subscribe(permanentTopic, 1, handleMensagemServidor)
			return nil
		}
		return fmt.Errorf("resposta de login inesperada: %s", resp.Comando)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("não foi possível obter ID do servidor (timeout)")
	}
}
func entrarNaFila() {
	dados := map[string]string{"cliente_id": meuID}
	payload, _ := json.Marshal(dados)

	topico := fmt.Sprintf("clientes/%s/entrar_fila", meuID)
	token := mqttClient.Publish(topico, 0, false, payload)
	token.Wait()
}

var messageChan = make(chan protocolo.Mensagem, 10)

func handleMensagemServidor(client mqtt.Client, msg mqtt.Message) {
	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("Erro ao decodificar mensagem: %v", err)
		return
	}

	// Processa a mensagem
	processarMensagemServidor(mensagem)
}

func processarMensagemServidor(msg protocolo.Mensagem) {
	switch msg.Comando {
	case "LOGIN_OK":
		var dados map[string]string
		json.Unmarshal(msg.Dados, &dados)
		meuID = dados["cliente_id"]
		servidor := dados["servidor"]
		fmt.Printf("\n[LOGIN] Conectado ao servidor %s (ID: %s)\n", servidor, meuID)

		// Agora subscreve ao tópico correto com o ID
		topico := fmt.Sprintf("clientes/%s/eventos", meuID)
		token := mqttClient.Subscribe(topico, 0, handleMensagemServidor)
		token.Wait()

	case "AGUARDANDO_OPONENTE":
		fmt.Printf("\n[MATCHMAKING] Aguardando oponente...\n> ")

	case "PARTIDA_ENCONTRADA":
		var dados protocolo.DadosPartidaEncontrada
		json.Unmarshal(msg.Dados, &dados)
		salaAtual = dados.SalaID
		oponenteID = dados.OponenteID
		oponenteNome = dados.OponenteNome

		fmt.Printf("\n[PARTIDA] Partida encontrada contra '%s'!\n", oponenteNome)
		fmt.Println("Use /comprar para adquirir seu pacote inicial de cartas.")

		// Subscreve aos eventos da partida
		topicoPartida := fmt.Sprintf("partidas/%s/eventos", salaAtual)
		if token := mqttClient.Subscribe(topicoPartida, 0, handleEventoPartida); token.Wait() && token.Error() != nil {
			log.Printf("Erro ao se inscrever no tópico da partida: %v", token.Error())
		}

	case "TROCA_CONCLUIDA":
		var resp protocolo.TrocarCartasResp
		json.Unmarshal(msg.Dados, &resp)
		fmt.Printf("\n[TROCA] %s\n", resp.Mensagem)
		mostrarCartas() // Mostra o inventário atualizado
		fmt.Print("> ")

	case "PACOTE_RESULTADO":
		var dados protocolo.ComprarPacoteResp
		json.Unmarshal(msg.Dados, &dados)
		meuInventario = dados.Cartas

		fmt.Printf("\n╔═══════════════════════════════════════╗\n")
		fmt.Printf("║   PACOTE RECEBIDO!                    ║\n")
		fmt.Printf("║   Você recebeu %d cartas              ║\n", len(dados.Cartas))
		fmt.Printf("╚═══════════════════════════════════════╝\n")

		fmt.Println("\nSuas cartas:")
		for i, carta := range dados.Cartas {
			fmt.Printf("  %d. %s %s - Poder: %d (Raridade: %s) [ID: %s]\n",
				i+1, carta.Nome, carta.Naipe, carta.Valor, carta.Raridade, carta.ID)
		}
		fmt.Print("> ")

	case "SISTEMA":
		var dados protocolo.DadosErro
		json.Unmarshal(msg.Dados, &dados)
		fmt.Printf("\n[SISTEMA] %s\n> ", dados.Mensagem)

	case "ERRO":
		var dados protocolo.DadosErro
		json.Unmarshal(msg.Dados, &dados)
		fmt.Printf("\n[ERRO] %s\n> ", dados.Mensagem)

	default:
		fmt.Printf("\n[DEBUG] Comando não reconhecido: %s\n> ", msg.Comando)
	}
}

func handleEventoPartida(client mqtt.Client, msg mqtt.Message) {
	var mensagem protocolo.Mensagem
	if err := json.Unmarshal(msg.Payload(), &mensagem); err != nil {
		log.Printf("Erro ao decodificar evento da partida: %v", err)
		return
	}

	switch mensagem.Comando {
	case "PARTIDA_INICIADA":
		var dados map[string]string
		json.Unmarshal(mensagem.Dados, &dados)
		fmt.Printf("\n╔═══════════════════════════════════════╗\n")
		fmt.Printf("║   PARTIDA INICIADA!                   ║\n")
		fmt.Printf("╚═══════════════════════════════════════╝\n")
		fmt.Printf("\n%s\n", dados["mensagem"])
		fmt.Println("Use /jogar <ID_da_carta> para jogar uma carta")
		fmt.Println("Use /cartas para ver suas cartas")
		fmt.Print("> ")

	case "ATUALIZACAO_JOGO":
		var dados protocolo.DadosAtualizacaoJogo
		json.Unmarshal(mensagem.Dados, &dados)

		fmt.Printf("\n--- RODADA %d ---\n", dados.NumeroRodada)
		fmt.Println(dados.MensagemDoTurno)

		if len(dados.UltimaJogada) > 0 {
			fmt.Println("\nCartas na mesa:")
			for nome, carta := range dados.UltimaJogada {
				fmt.Printf("  %s: %s %s (Poder: %d)\n", nome, carta.Nome, carta.Naipe, carta.Valor)
			}
		}

		if dados.VencedorJogada != "" && dados.VencedorJogada != "EMPATE" {
			fmt.Printf("\n🏆 Vencedor da jogada: %s\n", dados.VencedorJogada)
		}

		if dados.VencedorRodada != "" && dados.VencedorRodada != "EMPATE" {
			fmt.Printf("🎯 Vencedor da rodada: %s\n", dados.VencedorRodada)
		}

		if len(dados.ContagemCartas) > 0 {
			fmt.Println("\nCartas restantes:")
			for nome, qtd := range dados.ContagemCartas {
				fmt.Printf("  %s: %d cartas\n", nome, qtd)
			}
		}

		fmt.Println("-------------------")
		fmt.Print("> ")

	case "FIM_DE_JOGO":
		var dados protocolo.DadosFimDeJogo
		json.Unmarshal(mensagem.Dados, &dados)

		fmt.Printf("\n╔═══════════════════════════════════════╗\n")
		if dados.VencedorNome == "EMPATE" {
			fmt.Printf("║   FIM DE JOGO - EMPATE!               ║\n")
		} else {
			fmt.Printf("║   FIM DE JOGO!                        ║\n")
			fmt.Printf("║   Vencedor: %-25s ║\n", dados.VencedorNome)
		}
		fmt.Printf("╚═══════════════════════════════════════╝\n")
		fmt.Print("> ")

	case "RECEBER_CHAT":
		var dados protocolo.DadosReceberChat
		json.Unmarshal(mensagem.Dados, &dados)

		prefixo := dados.NomeJogador
		if dados.NomeJogador == meuNome {
			prefixo = "[VOCÊ]"
		}
		fmt.Printf("\n💬 %s: %s\n> ", prefixo, dados.Texto)
	}
}

func processarComando(entrada string) {
	partes := strings.Fields(entrada)
	if len(partes) == 0 {
		return
	}

	comando := partes[0]

	switch comando {
	case "/comprar":
		comprarPacote()

	case "/jogar":
		if len(partes) < 2 {
			fmt.Println("[ERRO] Uso: /jogar <ID_da_carta>")
			return
		}
		cartaID := partes[1]
		jogarCarta(cartaID)

	case "/cartas":
		mostrarCartas()

	case "/ajuda", "/help":
		mostrarAjuda()

	case "/sair":
		fmt.Println("Saindo...")
		os.Exit(0)
	case "/trocar":
		iniciarProcessoDeTroca()
	default:
		// Se não for um comando, envia como chat
		if salaAtual != "" {
			enviarChat(entrada)
		} else {
			fmt.Println("[ERRO] Comando não reconhecido. Use /ajuda para ver os comandos disponíveis.")
		}
	}
}

func comprarPacote() {
	if salaAtual == "" {
		fmt.Println("[ERRO] Você não está em uma partida.")
		return
	}

	dados := map[string]string{"cliente_id": meuID}
	mensagem := protocolo.Mensagem{
		Comando: "COMPRAR_PACOTE",
		Dados:   mustJSON(dados),
	}

	payload, _ := json.Marshal(mensagem)
	topico := fmt.Sprintf("partidas/%s/comandos", salaAtual)
	token := mqttClient.Publish(topico, 0, false, payload)
	token.Wait()

	fmt.Println("[INFO] Solicitação de compra enviada...")
}

func jogarCarta(cartaID string) {
	if salaAtual == "" {
		fmt.Println("[ERRO] Você não está em uma partida.")
		return
	}

	// Verifica se a carta existe no inventário
	cartaEncontrada := false
	var cartaNome string
	for _, carta := range meuInventario {
		if carta.ID == cartaID {
			cartaEncontrada = true
			cartaNome = carta.Nome
			break
		}
	}

	if !cartaEncontrada {
		fmt.Println("[ERRO] Carta não encontrada no seu inventário.")
		return
	}

	// Envia com cliente_id e carta_id
	dados := map[string]string{
		"cliente_id": meuID,
		"carta_id":   cartaID,
	}
	mensagem := protocolo.Mensagem{
		Comando: "JOGAR_CARTA",
		Dados:   mustJSON(dados),
	}

	payload, _ := json.Marshal(mensagem)
	topico := fmt.Sprintf("partidas/%s/comandos", salaAtual)
	token := mqttClient.Publish(topico, 0, false, payload)
	token.Wait()

	fmt.Printf("[INFO] Jogando carta: %s\n", cartaNome)
}

func enviarChat(texto string) {
	if salaAtual == "" {
		return
	}

	dados := map[string]string{
		"cliente_id": meuID,
		"texto":      texto,
	}
	mensagem := protocolo.Mensagem{
		Comando: "ENVIAR_CHAT",
		Dados:   mustJSON(dados),
	}

	payload, _ := json.Marshal(mensagem)
	topico := fmt.Sprintf("partidas/%s/comandos", salaAtual)
	token := mqttClient.Publish(topico, 0, false, payload)
	token.Wait()
}

func mostrarCartas() {
	if len(meuInventario) == 0 {
		fmt.Println("[INFO] Você não possui cartas. Use /comprar para adquirir um pacote.")
		return
	}

	fmt.Println("\n╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║                    SUAS CARTAS                            ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")

	for i, carta := range meuInventario {
		fmt.Printf("%2d. %-15s %s - Poder: %3d (Raridade: %s)\n",
			i+1, carta.Nome, carta.Naipe, carta.Valor, carta.Raridade)
		fmt.Printf("    ID: %s\n", carta.ID)
	}

	fmt.Printf("\nTotal: %d cartas\n", len(meuInventario))
}

func mostrarAjuda() {
	fmt.Println("\nComandos disponíveis:")
	fmt.Println("  /cartas                - Mostra suas cartas")
	fmt.Println("  /comprar               - Compra um novo pacote de cartas")
	fmt.Println("  /jogar <ID_da_carta>   - Joga uma carta da sua mão")
	fmt.Println("  /trocar                - Propõe uma troca de cartas com o oponente")
	fmt.Println("  /ajuda                 - Mostra esta lista de comandos")
	fmt.Println("  /sair                  - Sai do jogo")
	fmt.Println("  Qualquer outro texto será enviado como chat.")
}

func iniciarProcessoDeTroca() {
	if salaAtual == "" {
		fmt.Println("Você precisa estar em uma partida para trocar cartas.")
		return
	}
	if oponenteID == "" || oponenteNome == "" {
		fmt.Println("Não foi possível identificar seu oponente para a troca.")
		return
	}

	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("\n--- Propor Troca de Cartas ---")
	mostrarCartas()

	fmt.Print("Digite o ID da carta que você quer OFERECER: ")
	scanner.Scan()
	cartaOferecidaID := strings.TrimSpace(scanner.Text())

	fmt.Print("Digite o ID da carta do oponente que você quer RECEBER: ")
	scanner.Scan()
	cartaDesejadaID := strings.TrimSpace(scanner.Text())

	if cartaOferecidaID == "" || cartaDesejadaID == "" {
		fmt.Println("IDs das cartas não podem ser vazios. Abortando troca.")
		return
	}

	fmt.Printf("Enviando proposta de troca para %s...\n", oponenteNome)

	req := protocolo.TrocarCartasReq{
		IDJogadorOferta:     meuID,
		NomeJogadorOferta:   meuNome,
		IDJogadorDesejado:   oponenteID,
		NomeJogadorDesejado: oponenteNome,
		IDCartaOferecida:    cartaOferecidaID,
		IDCartaDesejada:     cartaDesejadaID,
	}

	msg := protocolo.Mensagem{
		Comando: "TROCAR_CARTAS_OFERTA",
		Dados:   mustJSON(req),
	}

	payload, _ := json.Marshal(msg)
	topico := fmt.Sprintf("partidas/%s/comandos", salaAtual)
	mqttClient.Publish(topico, 0, false, payload)
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
