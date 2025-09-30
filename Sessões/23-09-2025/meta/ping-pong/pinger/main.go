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

// pongHandler continua o mesmo, recebendo qualquer mensagem em 'pong/#'
var pongHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
	fmt.Printf("Recebido pong: '%s' do tópico: %s\n", msg.Payload(), msg.Topic())
}

func main() {
	// --- MUDANÇA PRINCIPAL: Geração de Client ID Único ---
	pingerID := fmt.Sprintf("pinger-%d", time.Now().UnixNano())
	fmt.Printf("Iniciando Pinger com ID: %s\n", pingerID)

	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(pingerID)
	// Define o handler para as mensagens de pong recebidas
	opts.SetDefaultPublishHandler(pongHandler)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}
	defer client.Disconnect(250)

	// Assina o tópico wildcard para receber pongs de todos os pongers
	if token := client.Subscribe("pong/#", 0, nil); token.Wait() && token.Error() != nil {
		fmt.Println(token.Error())
		os.Exit(1)
	}
	fmt.Println("Pinger inscrito em 'pong/#' para receber respostas.")

	// Loop para enviar pings para o tópico de broadcast
	go func() {
		for {
			text := fmt.Sprintf("ping from %s", pingerID)
			// --- MUDANÇA PRINCIPAL: Publica no tópico de broadcast ---
			fmt.Printf("Enviando ping para o tópico de broadcast: %s\n", broadcastTopic)
			token := client.Publish(broadcastTopic, 0, false, text)
			token.Wait()
			time.Sleep(5 * time.Second)
		}
	}()

	// Aguarda um sinal para encerrar
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("Encerrando o pinger.")
}
