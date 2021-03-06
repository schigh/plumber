package gcppubsub

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync"

	"cloud.google.com/go/pubsub"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/batchcorp/plumber/cli"
	"github.com/batchcorp/plumber/pb"
	"github.com/batchcorp/plumber/printer"
	"github.com/batchcorp/plumber/util"
)

func Read(opts *cli.Options) error {
	if err := validateReadOptions(opts); err != nil {
		return errors.Wrap(err, "unable to validate read options")
	}

	var mdErr error
	var md *desc.MessageDescriptor

	if opts.GCPPubSub.ReadOutputType == "protobuf" {
		md, mdErr = pb.FindMessageDescriptor(opts.GCPPubSub.ReadProtobufDir, opts.GCPPubSub.ReadProtobufRootMessage)
		if mdErr != nil {
			return errors.Wrap(mdErr, "unable to find root message descriptor")
		}
	}

	client, err := NewClient(opts)
	if err != nil {
		return errors.Wrap(err, "unable to create client")
	}

	r := &GCPPubSub{
		Options: opts,
		MsgDesc: md,
		Client:  client,
		log:     logrus.WithField("pkg", "rabbitmq/read.go"),
	}

	return r.Read()
}

func (g *GCPPubSub) Read() error {
	g.log.Info("Listening for message(s) ...")

	sub := g.Client.Subscription(g.Options.GCPPubSub.ReadSubscriptionId)

	// Receive launches several goroutines to exec func, need to use a mutex
	var m sync.Mutex

	lineNumber := 1

	// Standard way to cancel Receive in gcp's pubsub
	cctx, cancel := context.WithCancel(context.Background())

	if err := sub.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
		m.Lock()
		defer m.Unlock()

		if g.Options.GCPPubSub.ReadAck {
			defer msg.Ack()
		}

		if g.Options.GCPPubSub.ReadOutputType == "protobuf" {
			decoded, err := pb.DecodeProtobufToJSON(dynamic.NewMessage(g.MsgDesc), msg.Data)
			if err != nil {
				if !g.Options.GCPPubSub.ReadFollow {
					printer.Error(fmt.Sprintf("unable to decode protobuf message: %s", err))
					cancel()
					return
				}

				// Continue running
				printer.Error(fmt.Sprintf("unable to decode protobuf message: %s", err))
				return
			}

			msg.Data = decoded
		}

		var data []byte
		var convertErr error

		switch g.Options.GCPPubSub.ReadConvert {
		case "base64":
			_, convertErr = base64.StdEncoding.Decode(data, msg.Data)
		case "gzip":
			data, convertErr = util.Gunzip(msg.Data)
		default:
			data = msg.Data
		}

		if convertErr != nil {
			if !g.Options.GCPPubSub.ReadFollow {
				printer.Error(fmt.Sprintf("unable to complete conversion for message: %s", convertErr))
				cancel()
				return
			}

			// Continue running
			printer.Error(fmt.Sprintf("unable to complete conversion for message: %s", convertErr))
			return
		}

		str := string(data)

		if g.Options.GCPPubSub.ReadLineNumbers {
			str = fmt.Sprintf("%d: ", lineNumber) + str
			lineNumber++
		}

		printer.Print(str)

		if !g.Options.GCPPubSub.ReadFollow {
			cancel()
			return
		}
	}); err != nil {
		return errors.Wrap(err, "unable to complete msg receive")
	}

	g.log.Debug("Reader exiting")

	return nil
}

func validateReadOptions(opts *cli.Options) error {
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		return errors.New("GOOGLE_APPLICATION_CREDENTIALS must be set")
	}

	if opts.GCPPubSub.ReadOutputType == "protobuf" {
		if opts.GCPPubSub.ReadProtobufDir == "" {
			return errors.New("'--protobuf-dir' must be set when type " +
				"is set to 'protobuf'")
		}

		if opts.GCPPubSub.ReadProtobufRootMessage == "" {
			return errors.New("'--protobuf-root-message' must be when " +
				"type is set to 'protobuf'")
		}

		// Does given dir exist?
		if _, err := os.Stat(opts.GCPPubSub.ReadProtobufDir); os.IsNotExist(err) {
			return fmt.Errorf("--protobuf-dir '%s' does not exist", opts.GCPPubSub.ReadProtobufDir)
		}
	}

	return nil
}
