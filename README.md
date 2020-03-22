# mackerel-plugin-smartmeter

USB接続のWi-SUNモジュールを用いてスマートメーターから情報取得するMackerelプラグイン

## 概要

USB接続のWi-SUNモジュールを用いて自宅の電力スマートメーターにアクセスし、[Mackerel](https://mackerel.io/ja/)を用いて電力値・電流値をグラフ化するプログラムです。

本プラグインで取得できる値は下記の通りです。

- 瞬時電力[W]
- 瞬時電流（R相・T相）[A]

## 利用の準備

まずPAN(Personal Area Network)のスキャンを行います。BルートIDとパスワードが必要です。

```
$ ./mackerel-plugin-smartmeter --device /dev/ttyACM0 --id ******************************** --password ************ --scan
  Channel:**
  Channel Page:**
  Pan ID:****
  Addr:****************
  LQI:8F
  Side:0
  PairID:********
FE80:0000:0000:0000:****:****:****:****
```

ここで「Channel」「Pan ID」および最終行のIPv6アドレスを記録しておき、コマンドライン引数として利用します。

```
$ ./mackerel-plugin-smartmeter --device /dev/ttyACM0 --id ******************************** --password ************ --channel ** --panid **** --ipaddr FE80:0000:0000:0000:****:****:****:****
smartmeter.power.value	296	1584861299
smartmeter.current.r	2	1584861299
smartmeter.current.t	1	1584861299
```

こんな風に値が得られればスマートメーターの値が取れています。Mackerelに登録しましょう。

## Mackerelに登録する

`mackerel-agent.conf`に動作確認したときと同じコマンドライン引数を書き写します。

```
[plugin.metrics.smartmeter]
command = "/home/pi/bin/mackerel-plugin-smartmeter --device /dev/ttyACM0 --id ******************************** --password ************ --channel ** --panid **** --ipaddr FE80:0000:0000:0000:****:****:****:****"
```

mackerel-agentをsystemd管理している場合、下記のように再起動します。

```
$ sudo systemctl restart mackerel-agent
```

しばらく待つと、瞬時電力と瞬時電流（R相・T相）のグラフが得られます。

![electric-power-consumption](https://raw.githubusercontent.com/hnw/mackerel-plugin-smartmeter/images/electric-power-consumption.png)

![electric-current](https://raw.githubusercontent.com/hnw/mackerel-plugin-smartmeter/images/electric-current.png)

## 注意点

Wi-SUNモジュールで用いられるSKコマンドにはスタンダードエディションとデュアルスタックエディション(DSE)とがあります。具体的には、ローム BP35C2や[UDG-1-WSNE](https://web116.jp/shop/netki/miruene_usb/miruene_usb_00.html)などがDSEのようです。これらのモジュールを利用する場合は本プログラムに`--dse`オプションを指定してください。

作者が現時点で動作確認しているWi-SUNモジュールは下記の通りです。

- [UDG-1-WSNE](https://web116.jp/shop/netki/miruene_usb/miruene_usb_00.html)
