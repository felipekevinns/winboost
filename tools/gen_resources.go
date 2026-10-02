package main

import (
	"image/png"
	"log"
	"os"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	f, err := os.Open("../icon.png")
	if err != nil {
		log.Fatal(err)
	}
	img, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	icon, err := winres.NewIconFromResizedImage(img, []int{256, 128, 64, 48, 32, 24, 16})
	if err != nil {
		log.Fatal(err)
	}
	rs := winres.ResourceSet{}
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		log.Fatal(err)
	}
	rs.SetManifest(winres.AppManifest{
		Description:         "WinBoost",
		Compatibility:       winres.Win10AndAbove,
		ExecutionLevel:      winres.RequireAdministrator,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})
	vi := version.Info{}
	vi.SetFileVersion("1.0.0.0")
	vi.SetProductVersion("1.0.0.0")
	for k, v := range map[string]string{
		version.ProductName:     "WinBoost",
		version.FileDescription: "WinBoost - otimização do Windows",
		version.CompanyName:     "WinBoost",
		version.LegalCopyright:  "© 2026 WinBoost",
		version.OriginalFilename: "WinBoost.exe",
		version.InternalName:    "WinBoost",
	} {
		_ = vi.Set(0x0416, k, v)
	}
	rs.SetVersionInfo(vi)
	out, err := os.Create("../rsrc_windows_amd64.syso")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := rs.WriteObject(out, winres.ArchAMD64); err != nil {
		log.Fatal(err)
	}
	log.Println("ok")
}
