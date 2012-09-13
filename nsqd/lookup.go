package main

import (
	"../nsq"
	"bitly/notify"
	"log"
	"net"
	"os"
	"time"
)

var notifyChannelChan = make(chan interface{})
var notifyTopicChan = make(chan interface{})
var syncTopicChan = make(chan *nsq.LookupPeer)
var lookupPeers = make([]*nsq.LookupPeer, 0)

func lookupRouter(lookupHosts []string, exitChan chan int) {
	tcpAddr, _ := net.ResolveTCPAddr("tcp", *tcpAddress)
	port := tcpAddr.Port
	netAddrs := getNetworkAddrs(tcpAddr)

	for _, host := range lookupHosts {
		log.Printf("LOOKUP: adding peer %s", host)
		lookupPeer := nsq.NewLookupPeer(host, func(lp *nsq.LookupPeer) {
			go func() {
				syncTopicChan <- lp
			}()
		})
		lookupPeers = append(lookupPeers, lookupPeer)
	}

	if len(lookupPeers) > 0 {
		notify.Start("new_channel", notifyChannelChan)
		notify.Start("new_topic", notifyTopicChan)
	}

	// for announcements, lookupd determines the host automatically
	ticker := time.Tick(15 * time.Second)
	for {
		select {
		case <-ticker:
			// send a heartbeat and read a response (read detects closed conns)
			for _, lookupPeer := range lookupPeers {
				log.Printf("LOOKUP: [%s] sending heartbeat", lookupPeer)
				_, err := lookupPeer.Command(nsq.Ping())
				if err != nil {
					log.Printf("ERROR: [%s] ping failed - %s", lookupPeer, err.Error())
				}
			}
		case newChannel := <-notifyChannelChan:
			// notify all nsqds that a new channel exists
			channel := newChannel.(*Channel)
			cmd := nsq.Announce(channel.topicName, channel.name, port, netAddrs)
			for _, lookupPeer := range lookupPeers {
				log.Printf("LOOKUP: [%s] new channel %s", lookupPeer, cmd)
				_, err := lookupPeer.Command(cmd)
				if err != nil {
					log.Printf("ERROR: [%s] announce failed - %s", lookupPeer, err.Error())
				}
			}
		case newTopic := <-notifyTopicChan:
			// notify all nsqds that a new topic exists
			topic := newTopic.(*Topic)
			cmd := nsq.Announce(topic.name, ".", port, netAddrs)
			for _, lookupPeer := range lookupPeers {
				log.Printf("LOOKUP: [%s] new topic %s", lookupPeer, cmd)
				_, err := lookupPeer.Command(cmd)
				if err != nil {
					log.Printf("ERROR: [%s] announce failed - %s", lookupPeer, err.Error())
				}
			}
		case lookupPeer := <-syncTopicChan:
			commands := make([]*nsq.Command, 0)
			// build all the commands first so we exit the lock(s) as fast as possible
			nsqd.RLock()
			for _, topic := range nsqd.topicMap {
				topic.RLock()
				if len(topic.channelMap) == 0 {
					commands = append(commands, nsq.Announce(topic.name, ".", port, netAddrs))
				} else {
					for _, channel := range topic.channelMap {
						commands = append(commands, nsq.Announce(channel.topicName, channel.name, port, netAddrs))
					}
				}
				topic.RUnlock()
			}
			nsqd.RUnlock()

			for _, cmd := range commands {
				log.Printf("LOOKUP: [%s] %s", lookupPeer, cmd)
				_, err := lookupPeer.Command(cmd)
				if err != nil {
					log.Printf("ERROR: [%s] announce %v failed - %s", lookupPeer, cmd, err.Error())
					break
				}
			}
		case <-exitChan:
			goto exit
		}
	}

exit:
	log.Printf("LOOKUP: closing")
	if len(lookupPeers) > 0 {
		notify.Stop("new_channel", notifyChannelChan)
		notify.Stop("new_topic", notifyTopicChan)
	}
}

func getNetworkAddrs(tcpAddr *net.TCPAddr) []string {
	netAddrs := make([]string, 0)
	if tcpAddr.IP.Equal(net.IPv4zero) || tcpAddr.IP.Equal(net.IPv6zero) || tcpAddr.IP.IsLoopback() {
		// we're listening on localhost
		netAddrs = append(netAddrs, "127.0.0.1")
	}

	// always append the hostname last
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("ERROR: failed to get hostname - %s", err.Error())
	}
	netAddrs = append(netAddrs, hostname)

	return netAddrs
}
