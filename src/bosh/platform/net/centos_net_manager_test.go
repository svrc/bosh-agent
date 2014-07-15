package net_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	boshlog "bosh/logger"
	. "bosh/platform/net"
	fakearp "bosh/platform/net/arp/fakes"
	fakenet "bosh/platform/net/fakes"
	boship "bosh/platform/net/ip"
	fakeip "bosh/platform/net/ip/fakes"
	boshsettings "bosh/settings"
	fakesys "bosh/system/fakes"
)

func init() {
	const expectedCentosDHCPConfig = `# Generated by bosh-agent

option rfc3442-classless-static-routes code 121 = array of unsigned integer 8;

send host-name "<hostname>";

request subnet-mask, broadcast-address, time-offset, routers,
	domain-name, domain-name-servers, domain-search, host-name,
	netbios-name-servers, netbios-scope, interface-mtu,
	rfc3442-classless-static-routes, ntp-servers;

prepend domain-name-servers zz.zz.zz.zz;
prepend domain-name-servers yy.yy.yy.yy;
prepend domain-name-servers xx.xx.xx.xx;
`

	const expectedCentosIfcfg = `DEVICE=eth0
BOOTPROTO=static
IPADDR=192.168.195.6
NETMASK=255.255.255.0
BROADCAST=192.168.195.255
GATEWAY=192.168.195.1
ONBOOT=yes`

	Describe("centos", func() {
		var (
			fs                     *fakesys.FakeFileSystem
			cmdRunner              *fakesys.FakeCmdRunner
			defaultNetworkResolver *fakenet.FakeDefaultNetworkResolver
			ipResolver             *fakeip.FakeIPResolver
			addressBroadcaster     *fakearp.FakeAddressBroadcaster
			netManager             NetManager
		)

		BeforeEach(func() {
			fs = fakesys.NewFakeFileSystem()
			cmdRunner = fakesys.NewFakeCmdRunner()
			defaultNetworkResolver = &fakenet.FakeDefaultNetworkResolver{}
			ipResolver = &fakeip.FakeIPResolver{}
			addressBroadcaster = &fakearp.FakeAddressBroadcaster{}
			logger := boshlog.NewLogger(boshlog.LevelNone)
			netManager = NewCentosNetManager(
				fs,
				cmdRunner,
				defaultNetworkResolver,
				ipResolver,
				addressBroadcaster,
				logger,
			)
		})

		Describe("SetupDhcp", func() {
			networks := boshsettings.Networks{
				"bosh": boshsettings.Network{
					Default: []string{"dns"},
					DNS:     []string{"xx.xx.xx.xx", "yy.yy.yy.yy", "zz.zz.zz.zz"},
				},
				"vip": boshsettings.Network{
					Default: []string{},
					DNS:     []string{"aa.aa.aa.aa"},
				},
			}

			ItBroadcastsMACAddresses := func() {
				It("starts broadcasting the MAC addresses", func() {
					errCh := make(chan error)

					err := netManager.SetupDhcp(networks, errCh)
					Expect(err).ToNot(HaveOccurred())

					<-errCh // wait for all arpings

					Expect(addressBroadcaster.BroadcastMACAddressesAddresses).To(Equal(
						[]boship.InterfaceAddress{
							// Resolve IP address because IP may not be known
							boship.NewResolvingInterfaceAddress("eth0", ipResolver),
						},
					))
				})
			}

			Context("when dhcp was not previously configured", func() {
				It("writes dhcp configuration", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
					Expect(dhcpConfig).ToNot(BeNil())
					Expect(dhcpConfig.StringContents()).To(Equal(expectedCentosDHCPConfig))
				})

				It("writes out DNS servers in reverse because DHCP prepend command will add them in correct order", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
					Expect(dhcpConfig).ToNot(BeNil())
					Expect(dhcpConfig.StringContents()).To(ContainSubstring(`
prepend domain-name-servers zz.zz.zz.zz;
prepend domain-name-servers yy.yy.yy.yy;
prepend domain-name-servers xx.xx.xx.xx;
`))
				})

				It("restarts network", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					Expect(len(cmdRunner.RunCommands)).To(Equal(1))
					Expect(cmdRunner.RunCommands[0]).To(Equal([]string{"service", "network", "restart"}))
				})

				ItBroadcastsMACAddresses()
			})

			Context("when dhcp was previously configured with different configuration", func() {
				BeforeEach(func() {
					fs.WriteFileString("/etc/dhcp/dhclient.conf", "fake-other-configuration")
				})

				It("updates dhcp configuration", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
					Expect(dhcpConfig).ToNot(BeNil())
					Expect(dhcpConfig.StringContents()).To(Equal(expectedCentosDHCPConfig))
				})

				It("restarts network", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					Expect(len(cmdRunner.RunCommands)).To(Equal(1))
					Expect(cmdRunner.RunCommands[0]).To(Equal([]string{"service", "network", "restart"}))
				})

				ItBroadcastsMACAddresses()
			})

			Context("when dhcp was previously configured with same configuration", func() {
				BeforeEach(func() {
					fs.WriteFileString("/etc/dhcp/dhclient.conf", expectedCentosDHCPConfig)
				})

				It("keeps dhcp configuration", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
					Expect(dhcpConfig).ToNot(BeNil())
					Expect(dhcpConfig.StringContents()).To(Equal(expectedCentosDHCPConfig))
				})

				It("does not restart network", func() {
					err := netManager.SetupDhcp(networks, nil)
					Expect(err).ToNot(HaveOccurred())

					Expect(len(cmdRunner.RunCommands)).To(Equal(0))
				})

				ItBroadcastsMACAddresses()
			})
		})

		Describe("SetupManualNetworking", func() {
			var errCh chan error

			BeforeEach(func() {
				errCh = make(chan error)
			})

			BeforeEach(func() {
				fs.WriteFileString("/sys/class/net/eth0/address", "22:00:0a:1f:ac:2a\n")
				fs.SetGlob("/sys/class/net/*", []string{"/sys/class/net/eth0"})
			})

			networks := boshsettings.Networks{
				"bosh": boshsettings.Network{
					Default: []string{"dns", "gateway"},
					IP:      "192.168.195.6",
					Netmask: "255.255.255.0",
					Gateway: "192.168.195.1",
					Mac:     "22:00:0a:1f:ac:2a",
					DNS:     []string{"10.80.130.2", "10.80.130.1"},
				},
			}

			It("sets up centos expected ifconfig", func() {
				err := netManager.SetupManualNetworking(networks, nil)
				Expect(err).ToNot(HaveOccurred())

				networkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-eth0")
				Expect(networkConfig).ToNot(BeNil())
				Expect(networkConfig.StringContents()).To(Equal(expectedCentosIfcfg))
			})

			It("sets up /etc/resolv.conf", func() {
				err := netManager.SetupManualNetworking(networks, nil)
				Expect(err).ToNot(HaveOccurred())

				resolvConf := fs.GetFileTestStat("/etc/resolv.conf")
				Expect(resolvConf).ToNot(BeNil())
				Expect(resolvConf.StringContents()).To(Equal(`# Generated by bosh-agent
nameserver 10.80.130.2
nameserver 10.80.130.1
`))
			})

			It("writes out DNS servers in order that was provided by the network", func() {
				err := netManager.SetupManualNetworking(networks, nil)
				Expect(err).ToNot(HaveOccurred())

				resolvConf := fs.GetFileTestStat("/etc/resolv.conf")
				Expect(resolvConf).ToNot(BeNil())
				Expect(resolvConf.StringContents()).To(ContainSubstring(`
nameserver 10.80.130.2
nameserver 10.80.130.1
`))
			})

			It("restarts networking", func() {
				err := netManager.SetupManualNetworking(networks, errCh)
				Expect(err).ToNot(HaveOccurred())

				fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-eth0")
				fs.GetFileTestStat("/etc/resolv.conf")

				<-errCh // wait for all arpings

				Expect(len(cmdRunner.RunCommands)).To(Equal(1))
				Expect(cmdRunner.RunCommands[0]).To(Equal([]string{"service", "network", "restart"}))
			})

			It("starts broadcasting the MAC addresses", func() {
				err := netManager.SetupManualNetworking(networks, errCh)
				Expect(err).ToNot(HaveOccurred())

				fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-eth0")
				fs.GetFileTestStat("/etc/resolv.conf")

				<-errCh // wait for all arpings

				Expect(addressBroadcaster.BroadcastMACAddressesAddresses).To(Equal([]boship.InterfaceAddress{
					boship.NewSimpleInterfaceAddress("eth0", "192.168.195.6"),
				}))
			})
		})
	})
}
