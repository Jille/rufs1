# -*- mode: ruby -*-
# vi: set ft=ruby :

Vagrant.configure("2") do |config|
  config.vm.box = "generic/debian9"
  config.vm.provision "shell", path: "vagrant-bootstrap.sh"

  config.vm.define "rufs-master" do |rufsmaster|
    rufsmaster.vm.hostname = "rufs-master"
    rufsmaster.vm.network "private_network", ip: "198.51.100.42",
        virtualbox__intnet: "rufs-peers"
    rufsmaster.vm.synced_folder ".vagrant-syncdir/", "/public/"
    rufsmaster.vm.provision "shell", path: "vagrant-init.sh", args: "master"
  end

  config.vm.define "rufs-client" do |rufsclient|
    rufsclient.vm.hostname = "rufs-client"
    rufsclient.vm.network "private_network", ip: "198.51.100.137",
        virtualbox__intnet: "rufs-peers"
    rufsclient.vm.synced_folder ".vagrant-syncdir/", "/public/"
    rufsclient.vm.provision "shell", path: "vagrant-init.sh", args: "client"
  end
end
