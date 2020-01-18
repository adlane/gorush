package gorush

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/net/context"

	firebase "firebase.google.com/go"
	fcm "firebase.google.com/go/messaging"
	"google.golang.org/api/option"

	"github.com/sirupsen/logrus"
)

// InitFCMClient use for initialize FCM Client.
func InitFCMClient(credentialsFile string) (*fcm.Client, error) {
	if credentialsFile == "" {
		return nil, errors.New("Missing Credentials File")
	}

	if FCMClient == nil {
		opt := option.WithCredentialsFile(credentialsFile)
		ctx := context.Background()
		if app, err := firebase.NewApp(ctx, nil, opt); err != nil {
			return nil, err
		} else {
			if client, err := app.Messaging(ctx); err != nil {
				return nil, err
			} else {
				FCMClient = client
			}
		}
	}

	return FCMClient, nil
}

// GetAndroidNotification use for define Android notification.
// HTTP Connection Server Reference for Android
// https://firebase.google.com/docs/cloud-messaging/http-server-ref
func getAndroidNotifications(req PushNotification) []*fcm.Message {
	res := []*fcm.Message{}
	if len(req.Topic) > 0 {
		notification := getAndroidNotification(req)
		notification.Topic = req.Topic
		res = append(res, notification)
	} else if len(req.Condition) > 0 {
		notification := getAndroidNotification(req)
		notification.Condition = req.Condition
		res = append(res, notification)
	} else {
		for _, token := range req.Tokens {
			notification := getAndroidNotification(req)
			notification.Token = token
			res = append(res, notification)
		}
	}

	return res
}

func getAndroidNotification(req PushNotification) *fcm.Message {
	notif := &fcm.Notification{
		Title: req.Title,
		// ImageURL string `json:"image,omitempty"`
	}

	androidNotification := &fcm.AndroidNotification{
		Title:       req.Title,
		Icon:        req.Notification.Icon,
		Color:       req.Notification.Color,
		ClickAction: req.Notification.ClickAction,
		ChannelID:   req.Notification.ChannelID,
		Tag:         req.Notification.Tag,
		// ImageURL     string   `json:"image,omitempty"`
	}

	if v, ok := req.Sound.(string); ok && len(v) > 0 {
		androidNotification.Sound = v
	}

	if req.Notification.TitleLocKey != "" {
		androidNotification.TitleLocKey = req.Notification.TitleLocKey
		androidNotification.TitleLocArgs = []string{req.Notification.TitleLocArgs}
	}

	if req.Notification.BodyLocKey != "" {
		androidNotification.BodyLocKey = req.Notification.BodyLocKey
		androidNotification.BodyLocArgs = []string{req.Notification.BodyLocArgs}
	}

	// Set request message if body is empty
	if len(req.Message) > 0 {
		androidNotification.Body = req.Message
		notif.Body = req.Message
	}

	androidConfig := &fcm.AndroidConfig{
		CollapseKey:           req.CollapseKey,
		Priority:              req.Priority, // one of "normal" or "high"
		RestrictedPackageName: req.RestrictedPackageName,
		Notification:          androidNotification,
	}

	if req.TimeToLive != nil {
		ttl := time.Duration(*req.TimeToLive) * time.Hour
		androidConfig.TTL = &ttl
	}

	notification := &fcm.Message{
		Android:      androidConfig,
		Notification: notif,
	}

	notification.Data = make(map[string]string)
	if len(req.Data) > 0 {
		for k, v := range req.Data {
			notification.Data[k] = fmt.Sprintf("%v", v)
		}
	}
	notification.Data["title"] = req.Title
	notification.Data["body"] = req.Message

	if len(req.Title) > 0 {
		notification.Notification.Title = req.Title
	}

	return notification
}

// PushToAndroid provide send notification to Android server.
func PushToAndroid(req PushNotification) bool {
	LogAccess.Debug("Start push notification for Android")

	var (
		client     *fcm.Client
		retryCount = 0
		maxRetry   = PushConf.Android.MaxRetry
	)

	if req.Retry > 0 && req.Retry < maxRetry {
		maxRetry = req.Retry
	}

	// check message
	err := CheckMessage(req)

	if err != nil {
		LogError.Error("request error: " + err.Error())
		return false
	}

Retry:
	var isError = false

	notifications := getAndroidNotifications(req)

	client, err = InitFCMClient(PushConf.Firebase.CredentialsFile)
	if err != nil {
		// FCM server error
		LogError.Error("FCM server error: " + err.Error())
		return false
	}

	res, err := client.SendAll(req.ctx, notifications)
	if err != nil {
		// Send Message error
		LogError.Error("FCM server send message error: " + err.Error())
		return false
	}

	LogAccess.Debug(fmt.Sprintf("Android Success count: %d, Failure count: %d", res.SuccessCount, res.FailureCount))
	StatStorage.AddAndroidSuccess(int64(res.SuccessCount))
	StatStorage.AddAndroidError(int64(res.FailureCount))

	var newTokens []string
	// result from Send messages to topics
	if req.IsTopic() {
		to := ""
		if req.Topic != "" {
			to = req.Topic
		} else {
			to = req.Condition
		}
		LogAccess.Debug("Send Topic Message: ", to)
		result := res.Responses[0]

		// failure
		if result.Error != nil {
			isError = true
			LogPush(FailedPush, to, req, result.Error)
			if PushConf.Core.Sync {
				req.AddLog(getLogPushEntry(FailedPush, to, req, result.Error))
			}
		} else {
			LogPush(SucceededPush, to, req, nil)
		}
	} else {
		// result from Send messages to specific devices
		for k, result := range res.Responses {
			to := ""
			if k < len(req.Tokens) {
				to = req.Tokens[k]
			}

			if result.Error != nil {
				isError = true
				newTokens = append(newTokens, to)
				LogPush(FailedPush, to, req, result.Error)
				if PushConf.Core.Sync {
					req.AddLog(getLogPushEntry(FailedPush, to, req, result.Error))
				} else if PushConf.Core.FeedbackURL != "" {
					go func(logger *logrus.Logger, log LogPushEntry, url string) {
						err := DispatchFeedback(log, url)
						if err != nil {
							logger.Error(err)
						}
					}(LogError, getLogPushEntry(FailedPush, to, req, result.Error), PushConf.Core.FeedbackURL)
				}
				continue
			}

			LogPush(SucceededPush, to, req, nil)
		}
	}

	if isError && retryCount < maxRetry {
		retryCount++

		// resend fail token
		req.Tokens = newTokens
		goto Retry
	}

	return isError
}
