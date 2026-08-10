package shClient

type SHCPacket struct {
	Length uint32
	Data   []byte
}

type StateMessagePacket struct {
	SenderID    uint16
	DestID      uint16
	PD          uint8
	TransID     uint8
	SenderSubID uint8
	DestSubID   uint8
	DataLength  uint16
}

type PacketSubIDLength struct {
	DestSubID  uint8
	DataLength uint16
}

type ReceivePacket struct {
	ClientID uint16
	ID       uint16
	PD       uint8
	Zero1    uint8
	Zero2    uint8
	SubID    uint8
	Length   uint16
}
