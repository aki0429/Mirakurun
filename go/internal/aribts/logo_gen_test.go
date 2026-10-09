// logo_gen_test.go: Node 参照実装で生成した期待値 (生成スクリプト: .hermes-development/scratch/aribts/gen_logo.js)
package aribts

var logoPNGInput = mustHex("89504e470d0a1a0a4948445200000010000000100803000000aabbccdddeadbeef01020304")
var logoPNGWant = mustHex("89504e470d0a1a0a4948445200000010000000100803000000aabbccdddeadbeef00000183504c5445000000ff000000ff00ffff000000ffff00ff00ffffffffff000000aa000000aa00aaaa000000aaaa00aa00aaaaaaaaaa0000550055000055550055aa0055ff00aa5500aaff00ff5500ffaa5500005500555500aa5500ff5555005555555555aa5555ff55aa0055aa5555aaaa55aaff55ff0055ff5555ffaa55ffffaa0055aa00ffaa5500aa5555aa55aaaa55ffaaaa55aaaaffaaff00aaff55aaffaaaaffffff0055ff00ffff5500ff5555ff55aaff55ffffaa00ffaa55ffaaaaffaaffffff55ffffff000000ff000000ff00ffff000000ffff00ff00ffffffffffaa000000aa00aaaa000000aaaa00aa00aaaaaaaaaa0000550055000055550055aa0055ff00aa5500aaff00ff5500ffaa5500005500555500aa5500ff5555005555555555aa5555ff55aa0055aa5555aaaa55aaff55ff0055ff5555ffaa55ffffaa0055aa00ffaa5500aa5555aa55aaaa55ffaaaa55aaaaffaaff00aaff55aaffaaaaffffff0055ff00ffff5500ff5555ff55aaff55ffffaa00ffaa55ffaaaaffaaffffff55ffffff06dd277b0000008174524e53ffffffffffffffff00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080808080807b70f76f01020304")
var logoModuleInput = mustHex("0100010123027cc27cc1ffcc7cc17cc1ffcd000411223344")
var cdtLogoInput = mustHex("03001104560003aabbcc")

// logoModuleWant: logo_id / services / data
var logoModuleWant = struct {
	LogoID   int
	Services [][3]int
	Data     []byte
}{
	LogoID: 291,
	Services: [][3]int{
		{31938, 31937, 65484},
		{31937, 31937, 65485},
	},
	Data: mustHex("11223344"),
}

// cdtLogoWant: logo_type / logo_id / data
var cdtLogoWant = struct {
	LogoType, LogoID int
	Data             []byte
}{
	LogoType: 3, LogoID: 17, Data: mustHex("aabbcc"),
}
