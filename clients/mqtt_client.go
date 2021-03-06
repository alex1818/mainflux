package clients

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mainflux/mainflux/config"
	"github.com/mainflux/mainflux/db"
	"github.com/mainflux/mainflux/models"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/krylovsk/gosenml"
	"gopkg.in/mgo.v2/bson"
)

type (
	// ChannelWriteStatus is a type of Go chan
	// that is used to communicate request status
	ChannelWriteStatus struct {
		Nb  int
		Str string
	}

	// MqttConn struct
	MqttConn struct {
		Opts   *mqtt.ClientOptions
		Client mqtt.Client
	}
)

var (
	// MqttClient is used in HTTP server to communicate HTTP value updates/requests
	MqttClient mqtt.Client

	// WriteStatusChannel is used by HTTP server to communicate req status
	WriteStatusChannel chan ChannelWriteStatus
)

//define a function for the default message handler
var msgHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
	fmt.Printf("TOPIC: %s\n", msg.Topic())
	fmt.Printf("MSG: %s\n", msg.Payload())

	s := strings.Split(msg.Topic(), "/")
	chanID := s[len(s)-1]
	status := WriteChannel(chanID, msg.Payload())

	// Send status to HTTP publisher
	WriteStatusChannel <- status

	fmt.Println(status)
}

// MqttSub function - we subscribe to topic `mainflux/#` (no trailing `/`)
func (mqc *MqttConn) MqttSub(cfg config.Config) {
	// Create a ClientOptions struct setting the broker address, clientid, turn
	// off trace output and set the default message handler
	mqc.Opts = mqtt.NewClientOptions().AddBroker("tcp://" + cfg.MQTTHost + ":" + strconv.Itoa(cfg.MQTTPort))
	mqc.Opts.SetClientID("mainflux")
	mqc.Opts.SetDefaultPublishHandler(msgHandler)

	//create and start a client using the above ClientOptions
	mqc.Client = mqtt.NewClient(mqc.Opts)
	if token := mqc.Client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	// Subscribe to all channels of all the devices and request messages to be delivered
	// at a maximum qos of zero, wait for the receipt to confirm the subscription
	// Topic is in the form:
	// mainflux/<channel_id>
	if token := mqc.Client.Subscribe("mainflux/#", 0, nil); token.Wait() && token.Error() != nil {
		fmt.Println(token.Error())
	}

	MqttClient = mqc.Client
	WriteStatusChannel = make(chan ChannelWriteStatus)
}

// WriteChannel function
// Generic function that updates the channel value.
// Can be called via various protocols.
func WriteChannel(id string, bodyBytes []byte) ChannelWriteStatus {
	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		fmt.Println("Error unmarshaling body")
	}

	Db := db.MgoDb{}
	Db.Init()
	defer Db.Close()

	s := ChannelWriteStatus{}

	// Check if someone is trying to change "id" key
	// and protect us from this
	if _, ok := body["id"]; ok {
		s.Nb = http.StatusBadRequest
		s.Str = "Invalid request: 'id' is read-only"
		return s
	}
	if _, ok := body["device"]; ok {
		println("Error: can not change device")
		s.Nb = http.StatusBadRequest
		s.Str = "Invalid request: 'device' is read-only"
		return s
	}
	if _, ok := body["created"]; ok {
		println("Error: can not change device")
		s.Nb = http.StatusBadRequest
		s.Str = "Invalid request: 'created' is read-only"
		return s
	}

	// Find the channel
	c := models.Channel{}
	if err := Db.C("channels").Find(bson.M{"id": id}).One(&c); err != nil {
		s.Nb = http.StatusNotFound
		s.Str = "Channel not found"
		return s
	}

	senmlDecoder := gosenml.NewJSONDecoder()
	var m gosenml.Message
	var err error
	if m, err = senmlDecoder.DecodeMessage(bodyBytes); err != nil {
		s.Nb = http.StatusBadRequest
		s.Str = "Invalid request: SenML can not be decoded"
		return s
	}

	m.BaseName = c.Name + m.BaseName
	m.BaseUnits = c.Unit + m.BaseUnits

	for _, e := range m.Entries {
		// Name = channelName + baseName + entryName
		e.Name = m.BaseName + e.Name

		// BaseTime
		e.Time = m.BaseTime + e.Time
		if e.Time <= 0 {
			e.Time += time.Now().Unix()
		}

		// BaseUnits
		if e.Units == "" {
			e.Units = m.BaseUnits
		}

		/** Insert entry in DB */
		colQuerier := bson.M{"id": id}
		change := bson.M{"$push": bson.M{"values": e}}
		err := Db.C("channels").Update(colQuerier, change)
		if err != nil {
			log.Print(err)
			s.Nb = http.StatusNotFound
			s.Str = "Not inserted"
			return s
		}
	}

	// Timestamp
	t := time.Now().UTC().Format(time.RFC3339)
	body["updated"] = t

	/** Then update channel timestamp */
	colQuerier := bson.M{"id": id}
	change := bson.M{"$set": bson.M{"updated": body["updated"]}}
	if err := Db.C("channels").Update(colQuerier, change); err != nil {
		log.Print(err)
		s.Nb = http.StatusNotFound
		s.Str = "Not updated"
		return s
	}

	s.Nb = http.StatusOK
	s.Str = "Updated"
	return s
}
