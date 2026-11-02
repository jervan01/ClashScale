package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"

	"fyne.io/systray"
)

//go:embed build/trayicon.png
var trayPNG []byte

// ICO 里打包的尺寸：Windows 会按 DPI 自动挑最接近的一张
var iconSizes = []int{16, 24, 32, 48, 64}

var (
	trayMu    sync.Mutex // 只保护下面这几个变量，持锁期间不调用 systray
	trayReady bool
	trayKey   string
	trayCache = map[string][]byte{}

	mToggle, mProxy, mRule, mGlobal, mDirect, mMesh *systray.MenuItem

	baseOnce sync.Once
	baseSrc  image.Image // 裁掉透明边距后的小狗图标；解码失败时为 nil
)

// 状态小圆点的颜色
var (
	colOff    = color.RGBA{141, 157, 167, 255} // 灰：未运行
	colProxy  = color.RGBA{46, 160, 100, 255}  // 绿：系统代理中
	colGlobal = color.RGBA{220, 60, 75, 255}   // 红：全局代理
	colCore   = color.RGBA{56, 189, 198, 255}  // 青：内核运行但系统代理未开
	colMesh   = color.RGBA{124, 92, 255, 255}  // 紫：已接入 tailnet
	colWarn   = color.RGBA{224, 169, 74, 255}  // 琥珀：连接中 / 需要登录
)

// 面积平均缩小到 n×n（按预乘 alpha 计算，边缘不会发黑）。不依赖第三方库。
func scaleDown(src image.Image, n int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw == 0 || sh == 0 {
		return dst
	}
	for y := 0; y < n; y++ {
		y0 := y * sh / n
		y1 := (y + 1) * sh / n
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < n; x++ {
			x0 := x * sw / n
			x1 := (x + 1) * sw / n
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, a, cnt uint64
			for yy := y0; yy < y1 && yy < sh; yy++ {
				for xx := x0; xx < x1 && xx < sw; xx++ {
					pr, pg, pb, pa := src.At(sb.Min.X+xx, sb.Min.Y+yy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / cnt) >> 8), G: uint8((g / cnt) >> 8),
				B: uint8((b / cnt) >> 8), A: uint8((a / cnt) >> 8),
			})
		}
	}
	return dst
}

// 裁掉四周的透明边距，让小狗撑满整个托盘位置。
func trimTransparent(src image.Image) image.Image {
	b := src.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a >= 0x8000 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return src
	}
	// 取成正方形，避免缩放变形
	w, h := maxX-minX+1, maxY-minY+1
	if w > h {
		d := w - h
		minY -= d / 2
		maxY = minY + w - 1
	} else if h > w {
		d := h - w
		minX -= d / 2
		maxX = minX + h - 1
	}
	rect := image.Rect(minX, minY, maxX+1, maxY+1)
	out := image.NewRGBA(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if image.Pt(x, y).In(b) {
				out.Set(x, y, src.At(x, y))
			}
		}
	}
	return out
}

func loadBaseIcon() {
	baseOnce.Do(func() {
		src, err := png.Decode(bytes.NewReader(trayPNG))
		if err != nil {
			return
		}
		baseSrc = trimTransparent(src)
	})
}

// side: 0 整圆，-1 只填左半，+1 只填右半。带抗锯齿，按 alpha 叠加。
func fillCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA, side int) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			px := float64(x) + 0.5
			if side < 0 && px > cx {
				continue
			}
			if side > 0 && px <= cx {
				continue
			}
			d := math.Hypot(px-cx, float64(y)+0.5-cy)
			a := r + 0.5 - d
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			dst := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(float64(c.R)*a + float64(dst.R)*(1-a)),
				G: uint8(float64(c.G)*a + float64(dst.G)*(1-a)),
				B: uint8(float64(c.B)*a + float64(dst.B)*(1-a)),
				A: uint8(255*a + float64(dst.A)*(1-a)),
			})
		}
	}
}

// 单个尺寸：小狗 + 右下角「双色状态点」。左半 = 代理，右半 = 组网。
func makeIconPNG(n int, left, right color.RGBA) []byte {
	var img *image.RGBA
	if baseSrc != nil {
		img = scaleDown(baseSrc, n)
	} else {
		img = image.NewRGBA(image.Rect(0, 0, n, n))
		h := float64(n) / 2
		fillCircle(img, h, h, h-1, color.RGBA{79, 70, 229, 255}, 0)
	}
	fn := float64(n)
	cx := fn * 0.72
	fillCircle(img, cx, cx, fn*0.28, color.RGBA{255, 255, 255, 255}, 0)
	fillCircle(img, cx, cx, fn*0.21, left, -1)
	fillCircle(img, cx, cx, fn*0.21, right, +1)

	var pb bytes.Buffer
	_ = png.Encode(&pb, img)
	return pb.Bytes()
}

// 多尺寸 ICO（每张都是 PNG 压缩格式，Vista 及以上均支持）。
func makeIcon(left, right color.RGBA) []byte {
	loadBaseIcon()
	pngs := make([][]byte, len(iconSizes))
	for i, n := range iconSizes {
		pngs[i] = makeIconPNG(n, left, right)
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, uint16(len(iconSizes))}) // ICONDIR
	offset := 6 + 16*len(iconSizes)
	for i, n := range iconSizes {
		w := byte(n)
		if n >= 256 {
			w = 0
		}
		ico.Write([]byte{w, w, 0, 0})
		binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, uint32(len(pngs[i])))
		binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		ico.Write(p)
	}
	return ico.Bytes()
}

func (a *App) startTray() { go systray.Run(a.onTrayReady, func() {}) }

func (a *App) stopTray() {
	trayMu.Lock()
	ready := trayReady
	trayMu.Unlock()
	if ready {
		systray.Quit()
	}
}

func (a *App) onTrayReady() {
	// 左键：打开主窗口（放进 goroutine，避免阻塞托盘线程），右键：弹菜单
	systray.SetOnTapped(func() { go a.showWindow() })

	show := systray.AddMenuItem("显示主窗口", "")
	systray.AddSeparator()
	toggle := systray.AddMenuItem("启动代理内核", "")
	proxy := systray.AddMenuItemCheckbox("系统代理", "", false)
	rule := systray.AddMenuItemCheckbox("规则模式", "", false)
	global := systray.AddMenuItemCheckbox("全局模式", "", false)
	direct := systray.AddMenuItemCheckbox("直连模式", "", false)
	systray.AddSeparator()
	mesh := systray.AddMenuItem("连接组网", "")
	systray.AddSeparator()
	allOn := systray.AddMenuItem("全部开启", "")
	allOff := systray.AddMenuItem("全部关闭", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("退出", "")

	trayMu.Lock()
	mToggle, mProxy, mRule, mGlobal, mDirect, mMesh = toggle, proxy, rule, global, direct, mesh
	trayReady = true
	trayMu.Unlock()

	go func() {
		for {
			select {
			case <-show.ClickedCh:
				go a.showWindow()
			case <-toggle.ClickedCh:
				go a.clash.ToggleCore()
			case <-proxy.ClickedCh:
				go a.clash.SetSystemProxy(!a.clash.snapshot().SystemProxy)
			case <-rule.ClickedCh:
				go a.clash.SetMode("rule")
			case <-global.ClickedCh:
				go a.clash.SetMode("global")
			case <-direct.ClickedCh:
				go a.clash.SetMode("direct")
			case <-mesh.ClickedCh:
				go func() {
					st := a.ts.GetStatus()
					a.ts.setConnected(!(st.Backend == "Running" || st.Connecting))
					a.ts.kickPoll()
				}()
			case <-allOn.ClickedCh:
				go a.SetScene("all")
			case <-allOff.ClickedCh:
				go a.SetScene("off")
			case <-quit.ClickedCh:
				go a.quit()
				return
			}
		}
	}()

	go a.updateTray()
}

func setCheck(m *systray.MenuItem, on bool) {
	if on {
		m.Check()
	} else {
		m.Uncheck()
	}
}

// 先在锁外算好状态；持锁只用来读写变量，调用 systray 时不持锁，避免死锁。
func (a *App) updateTray() {
	cs := a.clash.GetStatus()
	ts := a.ts.lastStatus()

	left, lname := colOff, "代理未运行"
	switch {
	case !cs.Running:
	case !cs.SystemProxy:
		left, lname = colCore, "内核运行中（系统代理未开启）"
	case cs.Mode == "global":
		left, lname = colGlobal, "全局代理"
	case cs.Mode == "direct":
		left, lname = colCore, "直连模式"
	default:
		left, lname = colProxy, "规则代理"
	}
	right, rname := colOff, "组网未连接"
	meshOn := false
	switch {
	case ts.Backend == "Running":
		right, rname, meshOn = colMesh, "组网已连接", true
	case ts.Connecting || ts.Backend == "Starting" || ts.Backend == "NeedsLogin":
		right, rname, meshOn = colWarn, "组网等待中", true
	}

	key := rgbKey(left) + "|" + rgbKey(right)
	trayMu.Lock()
	if !trayReady {
		trayMu.Unlock()
		return
	}
	changed := key != trayKey
	trayKey = key
	icon, ok := trayCache[key]
	toggle, proxy, rule, global, direct, mesh := mToggle, mProxy, mRule, mGlobal, mDirect, mMesh
	trayMu.Unlock()

	if changed {
		if !ok {
			icon = makeIcon(left, right)
			trayMu.Lock()
			trayCache[key] = icon
			trayMu.Unlock()
		}
		systray.SetIcon(icon)
	}
	systray.SetTooltip("ClashScale · " + lname + " · " + rname)
	if cs.Running {
		toggle.SetTitle("停止代理内核")
	} else {
		toggle.SetTitle("启动代理内核")
	}
	if meshOn {
		mesh.SetTitle("断开组网")
	} else {
		mesh.SetTitle("连接组网")
	}
	setCheck(proxy, cs.SystemProxy)
	setCheck(rule, cs.Mode == "rule")
	setCheck(global, cs.Mode == "global")
	setCheck(direct, cs.Mode == "direct")
}

func rgbKey(c color.RGBA) string {
	return string([]byte{c.R, c.G, c.B})
}
