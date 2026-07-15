FROM ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y sudo build-essential git golang && apt-get clean && rm -rf /var/lib/apt/lists/*

# RUN echo "ubuntu ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/ubuntu && chmod 0440 /etc/sudoers.d/ubuntu

RUN echo 'export PATH="$HOME/bin:$PATH"' >> /home/ubuntu/.profile
RUN echo 'source /workspaces/tako/build.env' >> /home/ubuntu/.profile

#RUN snap install golang --classic --channel=1.25/stable

# These lines are separated from the apt-get above as these are current dependencies but
# they may change in the future and are not stritly required for building tako itself.
RUN apt-get update && apt-get install -y libbtrfs-dev libgpgme-dev && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y shellcheck shfmt && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y ripgrep fish hx delta && apt-get clean && rm -rf /var/lib/apt/lists/*

USER ubuntu

CMD [ "/bin/bash" ]
