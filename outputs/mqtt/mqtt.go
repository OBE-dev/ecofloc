// Package mqtt implements a core.Output that publishes samples to an MQTT
// broker using the Eclipse Paho client.
package mqtt

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"ecofloc/core"
)

//go:embed mqtt.json
var configJSON []byte

// Config holds the MQTT output settings, loaded from mqtt.json.
type Config struct {
	Broker   string `json:"broker"`
	Topic    string `json:"topic"`
	ClientID string `json:"client_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoadConfig parses the embedded mqtt.json.
func LoadConfig() (Config, error) {
	var c Config
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return c, fmt.Errorf("mqtt: parsing mqtt.json: %w", err)
	}
	// Set default values if not specified
	if c.Broker == "" {
		c.Broker = "tcp://localhost:1883"
	}
	if c.Topic == "" {
		c.Topic = "ecofloc"
	}
	if c.ClientID == "" {
		c.ClientID = "ecofloc"
	}
	return c, nil
}

type Output struct {
	cfg    Config
	client mqtt.Client
}

// New instanciate an MQTT output using the settings from mqtt.json and connects to
// the MQTT broker
func New() (*Output, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	//set mqtt client options
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetConnectTimeout(5 * time.Second).
		SetAutoReconnect(true)
	//set username and password if provided
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	//create mqtt client
	client := mqtt.NewClient(opts)
	//connect to mqtt broker returns a handle to the connection
	handle := client.Connect()
	// wait for connection with timeout
	if !handle.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("mqtt: timed out connecting to %s", cfg.Broker)
	}
	// check if the connection failed
	if err := handle.Error(); err != nil {
		return nil, fmt.Errorf("mqtt: connecting to %s: %w", cfg.Broker, err)
	}

	//connection established
	return &Output{cfg: cfg, client: client}, nil
}

// Name implements core.Output.
func (o *Output) Name() string { return "mqtt" }

// Write publishes the batch of samples as a JSON payload to the configured topic.
func (o *Output) Write(samples []core.Sample) error {
	payload, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	//publish the payload to the topic
	handle := o.client.Publish(o.cfg.Topic, 0, false, payload)
	//wait for the publish to complete with a timeout
	if !handle.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt: publish to %q timed out", o.cfg.Topic)
	}
	//check if the publish failed
	return handle.Error()
}

// Close disconnects from the broker.
func (o *Output) Close() error {
	if o.client != nil && o.client.IsConnected() {
		// disconnect from the broker with a timeout for pending operations
		o.client.Disconnect(200)
	}
	return nil
}
