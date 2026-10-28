package smpp

import "fmt"

// CommandID is the command_id of a PDU header (SMPP 3.4 §5.1.2). A response has the request's value with bit 31 set.
type CommandID uint32

// The command_id values of SMPP 3.4 §5.1.2.1, Table 5-1.
const (
	CmdGenericNack         CommandID = 0x80000000
	CmdBindReceiver        CommandID = 0x00000001
	CmdBindReceiverResp    CommandID = 0x80000001
	CmdBindTransmitter     CommandID = 0x00000002
	CmdBindTransmitterResp CommandID = 0x80000002
	CmdQuerySM             CommandID = 0x00000003
	CmdQuerySMResp         CommandID = 0x80000003
	CmdSubmitSM            CommandID = 0x00000004
	CmdSubmitSMResp        CommandID = 0x80000004
	CmdDeliverSM           CommandID = 0x00000005
	CmdDeliverSMResp       CommandID = 0x80000005
	CmdUnbind              CommandID = 0x00000006
	CmdUnbindResp          CommandID = 0x80000006
	CmdReplaceSM           CommandID = 0x00000007
	CmdReplaceSMResp       CommandID = 0x80000007
	CmdCancelSM            CommandID = 0x00000008
	CmdCancelSMResp        CommandID = 0x80000008
	CmdBindTransceiver     CommandID = 0x00000009
	CmdBindTransceiverResp CommandID = 0x80000009
	CmdOutbind             CommandID = 0x0000000B
	CmdEnquireLink         CommandID = 0x00000015
	CmdEnquireLinkResp     CommandID = 0x80000015
	CmdSubmitMulti         CommandID = 0x00000021
	CmdSubmitMultiResp     CommandID = 0x80000021
	CmdAlertNotification   CommandID = 0x00000102
	CmdDataSM              CommandID = 0x00000103
	CmdDataSMResp          CommandID = 0x80000103
	responseBit            CommandID = 0x80000000
	vendorCommandIDFirst   CommandID = 0x00010200
	vendorCommandIDLast    CommandID = 0x000102FF
)

var commandNames = map[CommandID]string{
	CmdGenericNack:         "generic_nack",
	CmdBindReceiver:        "bind_receiver",
	CmdBindReceiverResp:    "bind_receiver_resp",
	CmdBindTransmitter:     "bind_transmitter",
	CmdBindTransmitterResp: "bind_transmitter_resp",
	CmdQuerySM:             "query_sm",
	CmdQuerySMResp:         "query_sm_resp",
	CmdSubmitSM:            "submit_sm",
	CmdSubmitSMResp:        "submit_sm_resp",
	CmdDeliverSM:           "deliver_sm",
	CmdDeliverSMResp:       "deliver_sm_resp",
	CmdUnbind:              "unbind",
	CmdUnbindResp:          "unbind_resp",
	CmdReplaceSM:           "replace_sm",
	CmdReplaceSMResp:       "replace_sm_resp",
	CmdCancelSM:            "cancel_sm",
	CmdCancelSMResp:        "cancel_sm_resp",
	CmdBindTransceiver:     "bind_transceiver",
	CmdBindTransceiverResp: "bind_transceiver_resp",
	CmdOutbind:             "outbind",
	CmdEnquireLink:         "enquire_link",
	CmdEnquireLinkResp:     "enquire_link_resp",
	CmdSubmitMulti:         "submit_multi",
	CmdSubmitMultiResp:     "submit_multi_resp",
	CmdAlertNotification:   "alert_notification",
	CmdDataSM:              "data_sm",
	CmdDataSMResp:          "data_sm_resp",
}

// String returns the name of the command in Table 5-1, "vendor(0x…)" for the range reserved for SMSC vendors, or
// "reserved(0x…)".
func (id CommandID) String() string {
	if name, ok := commandNames[id]; ok {
		return name
	}

	if req := id &^ responseBit; req >= vendorCommandIDFirst && req <= vendorCommandIDLast {
		return fmt.Sprintf("vendor(0x%08x)", uint32(id))
	}

	return fmt.Sprintf("reserved(0x%08x)", uint32(id))
}

// Defined reports whether Table 5-1 names the command. A command that is not defined is answered with generic_nack
// ESME_RINVCMDID (§3.3).
func (id CommandID) Defined() bool {
	_, ok := commandNames[id]

	return ok
}

// Supported reports whether this package decodes and encodes the body of the command. A defined command that is not
// supported can still be recognised by its header and answered with generic_nack ESME_RINVCMDID.
func (id CommandID) Supported() bool {
	switch id {
	case CmdGenericNack, CmdBindReceiver, CmdBindReceiverResp, CmdBindTransmitter, CmdBindTransmitterResp, CmdBindTransceiver,
		CmdBindTransceiverResp, CmdSubmitSM, CmdSubmitSMResp, CmdDeliverSM, CmdDeliverSMResp, CmdUnbind, CmdUnbindResp, CmdEnquireLink,
		CmdEnquireLinkResp:
		return true
	default:
		return false
	}
}

// IsResponse reports whether bit 31 of the command_id is set (§3.2.1).
func (id CommandID) IsResponse() bool {
	return id&responseBit != 0
}

// Response returns the command_id of the response to the request id (§5.1.2).
func (id CommandID) Response() CommandID {
	return id | responseBit
}

// IsBind reports whether id is one of the three bind requests (§4.1).
func (id CommandID) IsBind() bool {
	return id == CmdBindTransmitter || id == CmdBindReceiver || id == CmdBindTransceiver
}

// CommandStatus is the command_status of a PDU header (SMPP 3.4 §5.1.3).
type CommandStatus uint32

// The command_status values of SMPP 3.4 §5.1.3, Table 5-2.
const (
	StatusOK              CommandStatus = 0x00000000
	StatusInvMsgLen       CommandStatus = 0x00000001
	StatusInvCmdLen       CommandStatus = 0x00000002
	StatusInvCmdID        CommandStatus = 0x00000003
	StatusInvBndSts       CommandStatus = 0x00000004
	StatusAlyBnd          CommandStatus = 0x00000005
	StatusInvPrtFlg       CommandStatus = 0x00000006
	StatusInvRegDlvFlg    CommandStatus = 0x00000007
	StatusSysErr          CommandStatus = 0x00000008
	StatusInvSrcAdr       CommandStatus = 0x0000000A
	StatusInvDstAdr       CommandStatus = 0x0000000B
	StatusInvMsgID        CommandStatus = 0x0000000C
	StatusBindFail        CommandStatus = 0x0000000D
	StatusInvPaswd        CommandStatus = 0x0000000E
	StatusInvSysID        CommandStatus = 0x0000000F
	StatusCancelFail      CommandStatus = 0x00000011
	StatusReplaceFail     CommandStatus = 0x00000013
	StatusMsgQFul         CommandStatus = 0x00000014
	StatusInvSerTyp       CommandStatus = 0x00000015
	StatusInvNumDests     CommandStatus = 0x00000033
	StatusInvDLName       CommandStatus = 0x00000034
	StatusInvDestFlag     CommandStatus = 0x00000040
	StatusInvSubRep       CommandStatus = 0x00000042
	StatusInvEsmClass     CommandStatus = 0x00000043
	StatusCntSubDL        CommandStatus = 0x00000044
	StatusSubmitFail      CommandStatus = 0x00000045
	StatusInvSrcTON       CommandStatus = 0x00000048
	StatusInvSrcNPI       CommandStatus = 0x00000049
	StatusInvDstTON       CommandStatus = 0x00000050
	StatusInvDstNPI       CommandStatus = 0x00000051
	StatusInvSysTyp       CommandStatus = 0x00000053
	StatusInvRepFlag      CommandStatus = 0x00000054
	StatusInvNumMsgs      CommandStatus = 0x00000055
	StatusThrottled       CommandStatus = 0x00000058
	StatusInvSched        CommandStatus = 0x00000061
	StatusInvExpiry       CommandStatus = 0x00000062
	StatusInvDftMsgID     CommandStatus = 0x00000063
	StatusXTAppn          CommandStatus = 0x00000064
	StatusXPAppn          CommandStatus = 0x00000065
	StatusXRAppn          CommandStatus = 0x00000066
	StatusQueryFail       CommandStatus = 0x00000067
	StatusInvOptParStream CommandStatus = 0x000000C0
	StatusOptParNotAllwd  CommandStatus = 0x000000C1
	StatusInvParLen       CommandStatus = 0x000000C2
	StatusMissingOptParam CommandStatus = 0x000000C3
	StatusInvOptParamVal  CommandStatus = 0x000000C4
	StatusDeliveryFailure CommandStatus = 0x000000FE
	StatusUnknownErr      CommandStatus = 0x000000FF
)

var statusNames = map[CommandStatus]string{
	StatusOK:              "ESME_ROK",
	StatusInvMsgLen:       "ESME_RINVMSGLEN",
	StatusInvCmdLen:       "ESME_RINVCMDLEN",
	StatusInvCmdID:        "ESME_RINVCMDID",
	StatusInvBndSts:       "ESME_RINVBNDSTS",
	StatusAlyBnd:          "ESME_RALYBND",
	StatusInvPrtFlg:       "ESME_RINVPRTFLG",
	StatusInvRegDlvFlg:    "ESME_RINVREGDLVFLG",
	StatusSysErr:          "ESME_RSYSERR",
	StatusInvSrcAdr:       "ESME_RINVSRCADR",
	StatusInvDstAdr:       "ESME_RINVDSTADR",
	StatusInvMsgID:        "ESME_RINVMSGID",
	StatusBindFail:        "ESME_RBINDFAIL",
	StatusInvPaswd:        "ESME_RINVPASWD",
	StatusInvSysID:        "ESME_RINVSYSID",
	StatusCancelFail:      "ESME_RCANCELFAIL",
	StatusReplaceFail:     "ESME_RREPLACEFAIL",
	StatusMsgQFul:         "ESME_RMSGQFUL",
	StatusInvSerTyp:       "ESME_RINVSERTYP",
	StatusInvNumDests:     "ESME_RINVNUMDESTS",
	StatusInvDLName:       "ESME_RINVDLNAME",
	StatusInvDestFlag:     "ESME_RINVDESTFLAG",
	StatusInvSubRep:       "ESME_RINVSUBREP",
	StatusInvEsmClass:     "ESME_RINVESMCLASS",
	StatusCntSubDL:        "ESME_RCNTSUBDL",
	StatusSubmitFail:      "ESME_RSUBMITFAIL",
	StatusInvSrcTON:       "ESME_RINVSRCTON",
	StatusInvSrcNPI:       "ESME_RINVSRCNPI",
	StatusInvDstTON:       "ESME_RINVDSTTON",
	StatusInvDstNPI:       "ESME_RINVDSTNPI",
	StatusInvSysTyp:       "ESME_RINVSYSTYP",
	StatusInvRepFlag:      "ESME_RINVREPFLAG",
	StatusInvNumMsgs:      "ESME_RINVNUMMSGS",
	StatusThrottled:       "ESME_RTHROTTLED",
	StatusInvSched:        "ESME_RINVSCHED",
	StatusInvExpiry:       "ESME_RINVEXPIRY",
	StatusInvDftMsgID:     "ESME_RINVDFTMSGID",
	StatusXTAppn:          "ESME_RX_T_APPN",
	StatusXPAppn:          "ESME_RX_P_APPN",
	StatusXRAppn:          "ESME_RX_R_APPN",
	StatusQueryFail:       "ESME_RQUERYFAIL",
	StatusInvOptParStream: "ESME_RINVOPTPARSTREAM",
	StatusOptParNotAllwd:  "ESME_ROPTPARNOTALLWD",
	StatusInvParLen:       "ESME_RINVPARLEN",
	StatusMissingOptParam: "ESME_RMISSINGOPTPARAM",
	StatusInvOptParamVal:  "ESME_RINVOPTPARAMVAL",
	StatusDeliveryFailure: "ESME_RDELIVERYFAILURE",
	StatusUnknownErr:      "ESME_RUNKNOWNERR",
}

// String returns the error code name of Table 5-2, "vendor(0x…)" for the range reserved for SMSC vendor specific
// errors (0x400–0x4FF), or "reserved(0x…)".
func (s CommandStatus) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}

	if s >= 0x400 && s <= 0x4FF {
		return fmt.Sprintf("vendor(0x%08x)", uint32(s))
	}

	return fmt.Sprintf("reserved(0x%08x)", uint32(s))
}
