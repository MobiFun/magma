/*
Copyright (c) Facebook, Inc. and its affiliates.
All rights reserved.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.
*/

package servicers

import (
	"errors"
	"fmt"

	fegprotos "magma/feg/cloud/go/protos"
	"magma/feg/gateway/diameter"
	"magma/feg/gateway/services/swx_proxy/servicers"
	"magma/feg/gateway/services/testcore/hss/crypto"
	"magma/feg/gateway/services/testcore/hss/storage"
	lteprotos "magma/lte/cloud/go/protos"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/avp"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
)

// NewMAA outputs a multimedia authentication answer (MAA) to reply to a multimedia
// authentication request (MAR) message.
func NewMAA(srv *HomeSubscriberServer, msg *diam.Message) (*diam.Message, error) {
	err := ValidateMAR(msg)
	if err != nil {
		return msg.Answer(diam.MissingAVP), err
	}

	var mar servicers.MAR
	if err := msg.Unmarshal(&mar); err != nil {
		return msg.Answer(diam.UnableToComply), fmt.Errorf("MAR Unmarshal failed for message: %v failed: %v", msg, err)
	}

	subscriber, err := srv.store.GetSubscriberData(mar.UserName)
	if err != nil {
		if _, ok := err.(storage.UnknownSubscriberError); ok {
			return ConstructFailureAnswer(msg, mar.SessionID, srv.Config.Server, uint32(fegprotos.ErrorCode_USER_UNKNOWN)), err
		}
		return ConstructFailureAnswer(msg, mar.SessionID, srv.Config.Server, uint32(diam.UnableToComply)), err
	}

	err = srv.ResyncLteAuthSeq(subscriber, mar.AuthData.Authorization.Serialize())
	if err != nil {
		return ConvertAuthErrorToFailureMessage(err, msg, mar.SessionID, srv.Config.Server), err
	}

	if mar.AuthData.AuthScheme != servicers.SipAuthScheme_EAP_AKA {
		err = fmt.Errorf("Unsupported SIP authentication scheme: %s", mar.AuthData.AuthScheme)
		return ConstructFailureAnswer(msg, mar.SessionID, srv.Config.Server, uint32(diam.UnableToComply)), err
	}

	vectors, err := srv.GenerateSIPAuthVectors(subscriber, mar.NumberAuthItems)
	if err != nil {
		// If we generated any auth vectors successfully, then we can return them.
		// Otherwise, we must signal an error.
		// See 3GPP TS 29.273 section 8.1.2.1.2.
		if len(vectors) == 0 {
			return ConvertAuthErrorToFailureMessage(err, msg, mar.SessionID, srv.Config.Server), err
		}
	}

	return srv.NewSuccessfulMAA(msg, mar.SessionID, vectors), nil
}

// NewSuccessfulMAA outputs a successful multimedia authentication answer (MAA) to reply to an
// multimedia authentication request (MAR) message. It populates the MAA with all of the mandatory fields
// and adds the authentication vectors. See 3GPP TS 29.273 table 8.1.2.1.1/5.
func (srv *HomeSubscriberServer) NewSuccessfulMAA(msg *diam.Message, sessionID datatype.UTF8String, vectors []*crypto.SIPAuthVector) *diam.Message {
	maa := ConstructSuccessAnswer(msg, sessionID, srv.Config.Server)
	for _, vector := range vectors {
		authenticate := append(vector.Rand[:], vector.Autn[:]...)
		maa.NewAVP(avp.SIPAuthDataItem, avp.Mbit, diameter.Vendor3GPP, &diam.GroupedAVP{
			AVP: []*diam.AVP{
				diam.NewAVP(avp.SIPAuthenticationScheme, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.UTF8String(servicers.SipAuthScheme_EAP_AKA)),
				diam.NewAVP(avp.SIPAuthenticate, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.OctetString(authenticate)),
				diam.NewAVP(avp.SIPAuthorization, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.OctetString(vector.Xres[:])),
				diam.NewAVP(avp.ConfidentialityKey, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.OctetString(vector.ConfidentialityKey[:])),
				diam.NewAVP(avp.IntegrityKey, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.OctetString(vector.IntegrityKey[:])),
			},
		})
	}
	maa.NewAVP(avp.SIPNumberAuthItems, avp.Mbit|avp.Vbit, diameter.Vendor3GPP, datatype.Unsigned32(len(vectors)))
	return maa
}

// GenerateSIPAuthVectors returns a slice of `numVectors` SIP auth vectors for the subscriber.
func (srv *HomeSubscriberServer) GenerateSIPAuthVectors(subscriber *lteprotos.SubscriberData, numVectors uint32) ([]*crypto.SIPAuthVector, error) {
	var vectors = make([]*crypto.SIPAuthVector, 0, numVectors)
	for i := uint32(0); i < numVectors; i++ {
		vector, err := srv.GenerateSIPAuthVector(subscriber)
		if err != nil {
			return vectors, err
		}
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

// GenerateSIPAuthVector returns the SIP auth vector for the subscriber.
func (srv *HomeSubscriberServer) GenerateSIPAuthVector(subscriber *lteprotos.SubscriberData) (*crypto.SIPAuthVector, error) {
	lte := subscriber.Lte
	if err := ValidateLteSubscription(lte); err != nil {
		return nil, NewAuthRejectedError(err.Error())
	}
	if subscriber.State == nil {
		return nil, NewAuthRejectedError("Subscriber data missing subscriber state")
	}

	opc, err := srv.GetOrGenerateOpc(lte)
	if err != nil {
		return nil, err
	}
	ind := srv.AuthSqnInd // Store IND before incrementing
	err = srv.IncreaseSQN(subscriber)
	if err != nil {
		return nil, err
	}
	sqn := SeqToSqn(subscriber.State.LteAuthNextSeq, ind)
	vector, err := srv.Milenage.GenerateSIPAuthVector(lte.AuthKey, opc, sqn)
	if err != nil {
		return vector, NewAuthRejectedError(err.Error())
	}
	return vector, err
}

// ValidateMAR returns an error if the message is missing any mandatory AVPs.
// Mandatory AVPs are specified in 3GPP TS 29.273 Table 8.1.2.1.1/1.
func ValidateMAR(msg *diam.Message) error {
	if msg == nil {
		return errors.New("Message is nil")
	}
	_, err := msg.FindAVP(avp.UserName, dict.UndefinedVendorID)
	if err != nil {
		return errors.New("Missing IMSI in message")
	}
	_, err = msg.FindAVP(avp.SIPNumberAuthItems, diameter.Vendor3GPP)
	if err != nil {
		return errors.New("Missing SIP-Number-Auth-Items in message")
	}
	_, err = msg.FindAVP(avp.SIPAuthDataItem, diameter.Vendor3GPP)
	if err != nil {
		return errors.New("Missing SIP-Auth-Data-Item in message")
	}
	_, err = msg.FindAVP(avp.SIPAuthenticationScheme, diameter.Vendor3GPP)
	if err != nil {
		return errors.New("Missing SIP-Authentication-Scheme in message")
	}
	_, err = msg.FindAVP(avp.RATType, diameter.Vendor3GPP)
	if err != nil {
		return errors.New("Missing RAT type in message")
	}
	return nil
}
