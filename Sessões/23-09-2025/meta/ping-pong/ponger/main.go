package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	broker         = "tcp://localhost:1883"
	broadcastTopic = "ping/broadcast"
)

func main() {
	// --- MUDANÇA PRINCIPAL: Geração de Client ID Único ---
	pongerID := fmt.Sprintf("ponger-%d", time.Now().UnixNano())
	fmt.Printf("Iniciando Ponger com ID: %s\n", pongerID)

	// O handler de ping agora é uma closure para capturar o pongerID
	var pingHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Recebido ping: '%s' do tópico: %s\n", msg.Payload(), msg.Topic())

		// --- MUDANÇA PRINCIPAL: Responde no tópico único deste ponger ---
		pongTopic := fmt.Sprintf("pong/%s", pongerID)
		text := "pong"

		fmt.Printf("Enviando pong para o tópico: %s\n", pongTopic)
		token := client.Publish(pongTopic, 0, false, text)
		token.Wait()
	}

	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(pongerID)
	// O DefaultPublishHandler não é estritamente necessário aqui, mas é uma boa prática
	opts.SetDefaultPublishHandler(pingHandler)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}
	defer client.Disconnect(250)

	// --- MUDANÇA PRINCIPAL: Assina o tópico de broadcast ---
	if token := client.Subscribe(broadcastTopic, 0, pingHandler); token.Wait() && token.Error() != nil {
		fmt.Println(token.Error())
		os.Exit(1)
	}

	fmt.Printf("Ponger '%s' conectado e aguardando pings no tópico '%s'\n", pongerID, broadcastTopic)

	// Aguarda um sinal para encerrar
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("Encerrando o ponger.")
}
 