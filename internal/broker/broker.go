package broker

import (
	"fmt"

	mq "github.com/cheshir/go-mq/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

// MQ is used for the config and client information for the messaging queue.
type MQ struct {
	Config      mq.Config
	EnableDebug bool
}

// NewMQ returns a messaging with config and controller-runtime client.
func NewMQ(
	config mq.Config,
	enableDebug bool,
) *MQ {
	return &MQ{
		Config:      config,
		EnableDebug: enableDebug,
	}
}

// Publish publishes a message to a given queue
func (h *MQ) Publish(queue string, message []byte) error {
	opLog := ctrl.Log.WithName("broker").WithName("Publish")
	// no need to re-try here, this is on a cron schedule and the error is returned, cron will try again whenever it is set to run next
	messageQueue, err := mq.New(h.Config)
	if err != nil {
		opLog.Info(fmt.Sprintf("Failed to initialize message queue manager: %v", err))
		return err
	}
	defer messageQueue.Close()

	producer, err := messageQueue.AsyncProducer(queue)
	if err != nil {
		opLog.Info(fmt.Sprintf("Failed to get async producer: %v", err))
		return err
	}
	producer.Produce([]byte(fmt.Sprintf("%s", message)))
	return nil
}
