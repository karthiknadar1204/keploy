package postgresparser

import (
	"net"
	"time"

	// "time"

	// "time"
	// "sync"
	// "strings"

	"encoding/binary"
	// "encoding/json"
	"encoding/base64"
	// "fmt"
	// "github.com/jackc/pgproto3"
	"go.keploy.io/server/pkg/proxy/util"

	// "bytes"

	"errors"

	"go.keploy.io/server/pkg/hooks"
	"go.keploy.io/server/pkg/models"
	"go.uber.org/zap"
)

var Emoji = "\U0001F430" + " Keploy:"

func IsOutgoingPSQL(buffer []byte) bool {
	const ProtocolVersion = 0x00030000 // Protocol version 3.0

	if len(buffer) < 8 {
		// Not enough data for a complete header
		return false
	}

	// The first four bytes are the message length, but we don't need to check those

	// The next four bytes are the protocol version
	version := binary.BigEndian.Uint32(buffer[4:8])

	if version == 80877103 {
		return true
	}
	return version == ProtocolVersion
}

func ProcessOutgoingPSQL(requestBuffer []byte, clientConn, destConn net.Conn, h *hooks.Hook, logger *zap.Logger) {
	switch models.GetMode() {
	case models.MODE_RECORD:
		encodeStreamOutgoing(requestBuffer, clientConn, destConn, h, logger)

	case models.MODE_TEST:
		// decodeOutgoingPSQL(requestBuffer, clientConn, destConn, h, logger)
		decodeStreamOutgoing(requestBuffer, clientConn, destConn, h, logger)

	default:
		logger.Info("Invalid mode detected while intercepting outgoing http call", zap.Any("mode", models.GetMode()))
	}

}

type PSQLMessage struct {
	// Define fields to store the relevant information from the buffer
	ID      uint32
	Payload []byte
	Field1  string
	Field2  int
	// Add more fields as needed
}

func decodeBuffer(buffer []byte) (*PSQLMessage, error) {
	if len(buffer) < 6 {
		return nil, errors.New("invalid buffer length")
	}

	psqlMessage := &PSQLMessage{
		Field1: "test",
		Field2: 123,
	}

	// Decode the ID (4 bytes)
	psqlMessage.ID = binary.BigEndian.Uint32(buffer[:4])

	// Decode the payload length (2 bytes)
	payloadLength := binary.BigEndian.Uint16(buffer[4:6])

	// Check if the buffer contains the full payload
	if len(buffer[6:]) < int(payloadLength) {
		return nil, errors.New("incomplete payload in buffer")
	}

	// Extract the payload from the buffer
	psqlMessage.Payload = buffer[6 : 6+int(payloadLength)]

	return psqlMessage, nil
}

// This is the encoding function for the streaming postgres wiremessage
func encodeStreamOutgoing(requestBuffer []byte, clientConn, destConn net.Conn, h *hooks.Hook, logger *zap.Logger) error {

	// backend := pgproto3.NewBackend(pgproto3.NewChunkReader(clientConn), clientConn, clientConn)
	// frontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(destConn), destConn, destConn)
	pgRequests := []models.GenericPayload{}

	logger.Debug("Encoding outgoing generic call from postgres parser !!")
	bufStr := base64.StdEncoding.EncodeToString(requestBuffer)
	// }
	if bufStr != "" {

		pgRequests = append(pgRequests, models.GenericPayload{

			Origin: models.FromClient,
			Message: []models.OutputBinary{
				{
					Type: "binary",
					Data: bufStr,
				},
			},
		})
	}
	_, err := destConn.Write(requestBuffer)
	logger.Debug("Writing the request buffer to the destination server", zap.Any("requestBuffer", string(requestBuffer)))
	if err != nil {
		logger.Error("failed to write request message to the destination server", zap.Error(err))
		return err
	}

	pgResponses := []models.GenericPayload{}

	clientBufferChannel := make(chan []byte)
	destBufferChannel := make(chan []byte)
	errChannel := make(chan error)
	// read requests from client
	go ReadBuffConn(clientConn, clientBufferChannel, errChannel, logger)
	// read response from destination
	go ReadBuffConn(destConn, destBufferChannel, errChannel, logger)

	isPreviousChunkRequest := false

	// if clientBufferChannel is selected write to destination
	// if destBufferChannel is selected write to client
	for {

		select {
		case buffer := <-clientBufferChannel:
			// Write the request message to the destination
			_, err := destConn.Write(buffer)
			if err != nil {
				logger.Error("failed to write request message to the destination server", zap.Error(err))
				return err
			}
			// msg2, err := frontend.Receive(buffer)
			// if err != nil {
			// 	logger.Error(hooks.Emoji+"failed to read the response message from the destination server", zap.Error(err))
			// 	// return err
			// }
			// println("msg2 is ", msg2)
			// logger.Debug("the iteration for the generic request ends with no of genericReqs:" + strconv.Itoa(len(genericRequests)) + " and genericResps: " + strconv.Itoa(len(genericResponses)))

			if !isPreviousChunkRequest && len(pgRequests) > 0 && len(pgResponses) > 0 {
				h.AppendMocks(&models.Mock{
					Version: models.V1Beta2,
					Name:    "mocks",
					Kind:    models.Postgres,
					Spec: models.MockSpec{
						PostgresRequests:  pgRequests,
						PostgresResponses: pgResponses,
					},
				})
				pgRequests = []models.GenericPayload{}
				pgResponses = []models.GenericPayload{}

			}
			bufStr := base64.StdEncoding.EncodeToString(buffer)
			// }
			if bufStr != "" {

				pgRequests = append(pgRequests, models.GenericPayload{

					Origin: models.FromClient,
					Message: []models.OutputBinary{
						{
							Type: "binary",
							Data: bufStr,
						},
					},
				})
			}

			isPreviousChunkRequest = true
		case buffer := <-destBufferChannel:
			// Write the response message to the client
			_, err := clientConn.Write(buffer)
			if err != nil {

				logger.Error(hooks.Emoji+"failed to write response to the pg client", zap.Error(err))


				return err

			}

			bufStr := base64.StdEncoding.EncodeToString(buffer)
			// }
			if bufStr != "" {

				pgResponses = append(pgResponses, models.GenericPayload{

					Origin: models.FromServer,
					Message: []models.OutputBinary{
						{
							Type: "binary",
							Data: bufStr,
						},
					},
				})
			}
			// logger.Debug("the iteration for the generic response ends with no of genericReqs:" + strconv.Itoa(len(genericRequests)) + " and genericResps: " + strconv.Itoa(len(genericResponses)))
			isPreviousChunkRequest = false
		case err := <-errChannel:
			return err
		}

	}
}

// This is the decoding function for the postgres wiremessage
func decodeStreamOutgoing(requestBuffer []byte, clientConn, destConn net.Conn, h *hooks.Hook, logger *zap.Logger) error {
	pgRequests := [][]byte{requestBuffer}
	tcsMocks := h.GetTcsMocks()
	// change auth to md5 instead of scram
	ChangeAuthToMD5(tcsMocks, h, logger)
	// time.Sleep(3 * time.Second)
	for {
		// time.Sleep(5 * time.Second)
		err := clientConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		if err != nil {
			logger.Error(hooks.Emoji+"failed to set the read deadline for the pg client connection", zap.Error(err))
			return err
		}

		for {
			buffer, err := util.ReadBytes(clientConn)
			if netErr, ok := err.(net.Error); !(ok && netErr.Timeout()) && err != nil {
				logger.Error("failed to read the request message in proxy for generic dependency", zap.Error(err))
				// errChannel <- err
				return err
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				logger.Debug("the timeout for the client read in generic")
				break
			}

			pgRequests = append(pgRequests, buffer)
		}

		if len(pgRequests) == 0 {
			logger.Debug("the postgres request buffer is empty")

			continue
		}
		// bestMatchedIndx := 0
		// fuzzy match gives the index for the best matched generic mock

		matched, pgResponses := matchingPg(tcsMocks, pgRequests, h)

		if !matched {
			logger.Error("failed to match the dependency call from user application", zap.Any("request packets", len(pgRequests)))
			return errors.New("failed to match the dependency call from user application")
			// continue
		}
		for _, pgResponse := range pgResponses {
			encoded, _ := PostgresDecoder(pgResponse.Message[0].Data)

			_, err := clientConn.Write([]byte(encoded))
			if err != nil {
				logger.Error("failed to write request message to the client application", zap.Error(err))
				// errChannel <- err
				return err
			}
		}
		// }

		// update for the next dependency call

		pgRequests = [][]byte{}

	}

}

func ReadBuffConn(conn net.Conn, bufferChannel chan []byte, errChannel chan error, logger *zap.Logger) error {
	for {
		buffer, err := util.ReadBytes(conn)
		if err != nil {
			logger.Error("failed to read the packet message in proxy for generic dependency", zap.Error(err))
			errChannel <- err
			return err
		}
		bufferChannel <- buffer
	}

}
