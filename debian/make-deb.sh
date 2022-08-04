#!/bin/bash
set -x
tmp_dir=$(mktemp -d)

echo "Creating MetalBond Debian Package v$METALBOND_VERSION..."

make $ARCHITECTURE

mkdir -p $tmp_dir/metalbond/DEBIAN

mkdir -p $tmp_dir/metalbond/usr/sbin
mkdir -p $tmp_dir/metalbond/usr/share/metalbond/html
mkdir -p $tmp_dir/metalbond/usr/share/metalbond/systemd-units/

cp target/metalbond_$ARCHITECTURE $tmp_dir/metalbond/usr/sbin/metalbond
cp -ra target/html $tmp_dir/metalbond/usr/share/metalbond/
cp debian/systemd/* $tmp_dir/metalbond/usr/share/metalbond/systemd-units/

cp debian/no-src.control.custom $tmp_dir/metalbond/DEBIAN/control
sed -i "s/METALBOND_VERSION/$(echo $METALBOND_VERSION | cut -dv -f2)/" $tmp_dir/metalbond/DEBIAN/control
sed -i "s/ARCHITECTURE/$(echo $ARCHITECTURE | cut -dv -f2)/" $tmp_dir/metalbond/DEBIAN/control
cp debian/postinst $tmp_dir/metalbond/DEBIAN/
cp debian/preinst $tmp_dir/metalbond/DEBIAN/

( cd $tmp_dir && dpkg-deb --build metalbond )

mv $tmp_dir/metalbond.deb target/metalbond_$(echo $METALBOND_VERSION)_$ARCHITECTURE.deb

rm -rf $tmp_dir
