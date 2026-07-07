#!/usr/bin/env python3
"""生成 cogito-agent 的品牌 logo：README banner + 方形 avatar，各出 SVG（可縮放）與 PNG（點陣）。

與終端啟動 banner（internal/cmdutil/banner.go）、web logo 同一套 5×7 點陣字模與琥珀→鏽紅漸層。
用法：python3 docs/logo/generate.py   （輸出到本檔同目錄，需 Pillow 出 PNG）
"""
import os

FONT = {
    'C': ["11111","10000","10000","10000","10000","10000","11111"],
    'O': ["01110","10001","10001","10001","10001","10001","01110"],
    'G': ["01110","10001","10000","10111","10001","10001","01111"],
    'I': ["11111","00100","00100","00100","00100","00100","11111"],
    'T': ["11111","00100","00100","00100","00100","00100","00100"],
    'A': ["01110","10001","10001","11111","10001","10001","10001"],
    'E': ["11111","10000","10000","11110","10000","10000","11111"],
    'N': ["10001","11001","10101","10101","10011","10001","10001"],
    '-': ["00000","00000","00000","11111","00000","00000","00000"],
    ' ': ["00000"]*7,
}
G1, G2, G3, G4 = "#f7d060", "#ef9a4a", "#e8734a", "#b24a32"
GROUND, GROUND2, INK_DIM = "#141110", "#1b1512", "#8a7566"
STOPS = [(0.0,(247,208,96)),(0.34,(239,154,74)),(0.66,(232,115,74)),(1.0,(178,74,50))]
HERE = os.path.dirname(os.path.abspath(__file__))

def wcells(text):  # 每字 5 寬 + 1 字間，末尾不算
    return len(text)*6 - 1

def lerp(t):
    for i in range(len(STOPS)-1):
        t0,c0 = STOPS[i]; t1,c1 = STOPS[i+1]
        if t <= t1:
            f = (t-t0)/(t1-t0) if t1 > t0 else 0
            return tuple(round(c0[k]+(c1[k]-c0[k])*f) for k in range(3))
    return STOPS[-1][1]

# ---------------- SVG ----------------
def svg_rects(text, cell, x0, y0, fill, extra=""):
    out, col = [], 0
    for ch in text:
        g = FONT.get(ch, FONT[' '])
        for r in range(7):
            for c in range(5):
                if g[r][c] == '1':
                    x, y = x0+(col+c)*cell, y0+r*cell
                    out.append(f'<rect x="{x:.1f}" y="{y:.1f}" width="{cell}" height="{cell}" '
                               f'rx="{cell*0.14:.1f}" fill="{fill}"{extra}/>')
        col += 6
    return "\n".join(out)

def banner_svg():
    TEXT, cell = "COGITO-AGENT", 20
    lw, lh = wcells(TEXT)*cell, 7*cell
    pad_x, pad_top = 78, 74
    W = lw + 2*pad_x
    tag_y, sub_y = pad_top+lh+52, pad_top+lh+86
    H = sub_y + 40
    x0, y0, cx = pad_x, pad_top, W/2
    defs = (f'<linearGradient id="g" gradientUnits="userSpaceOnUse" x1="0" y1="{y0}" x2="0" y2="{y0+lh}">'
            f'<stop offset="0" stop-color="{G1}"/><stop offset="0.34" stop-color="{G2}"/>'
            f'<stop offset="0.66" stop-color="{G3}"/><stop offset="1" stop-color="{G4}"/></linearGradient>'
            f'<radialGradient id="bg" cx="0.5" cy="0.34" r="0.9">'
            f'<stop offset="0" stop-color="{GROUND2}"/><stop offset="0.62" stop-color="{GROUND}"/></radialGradient>')
    mono = 'ui-monospace,SFMono-Regular,Menlo,Consolas,monospace'
    echo = svg_rects(TEXT, cell, x0+6, y0+6, G3, ' fill-opacity="0.12"')
    main = svg_rects(TEXT, cell, x0, y0, "url(#g)")
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W:.0f} {H:.0f}" width="{W:.0f}" height="{H:.0f}" '
            f'role="img" aria-label="COGITO-AGENT — cogito, ergo ago">\n<defs>{defs}</defs>\n'
            f'<rect width="{W:.0f}" height="{H:.0f}" rx="18" fill="url(#bg)"/>\n'
            f'<g>{echo}</g>\n'
            f'<g>{main}</g>\n'
            f'<text x="{cx}" y="{tag_y}" text-anchor="middle" font-family="{mono}" font-size="30" '
            f'font-weight="600" fill="{G2}" letter-spacing="0.5">cogito, ergo ago</text>\n'
            f'<text x="{cx}" y="{sub_y}" text-anchor="middle" font-family="{mono}" font-size="15" '
            f'font-weight="500" fill="{INK_DIM}" letter-spacing="5">REASON &#183; ACT &#183; OBSERVE</text>\n</svg>\n')

def avatar_svg():
    S, cell = 512, 11
    top, bot = "COGITO", "AGENT"
    tw, bw, gap = wcells(top)*cell, wcells(bot)*cell, 3*cell
    y_top = (S-(7*cell*2+gap))/2
    y_bot = y_top+7*cell+gap
    x_top, x_bot = (S-tw)/2, (S-bw)/2
    defs = (f'<linearGradient id="g" gradientUnits="userSpaceOnUse" x1="0" y1="{y_top}" x2="0" y2="{y_bot+7*cell}">'
            f'<stop offset="0" stop-color="{G1}"/><stop offset="0.4" stop-color="{G2}"/>'
            f'<stop offset="0.72" stop-color="{G3}"/><stop offset="1" stop-color="{G4}"/></linearGradient>'
            f'<radialGradient id="bg" cx="0.5" cy="0.4" r="0.85">'
            f'<stop offset="0" stop-color="{GROUND2}"/><stop offset="0.7" stop-color="{GROUND}"/></radialGradient>')
    echo = (svg_rects(top,cell,x_top+4,y_top+4,G3,' fill-opacity="0.13"')+"\n"+
            svg_rects(bot,cell,x_bot+4,y_bot+4,G3,' fill-opacity="0.13"'))
    main = svg_rects(top,cell,x_top,y_top,"url(#g)")+"\n"+svg_rects(bot,cell,x_bot,y_bot,"url(#g)")
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {S} {S}" width="{S}" height="{S}" '
            f'role="img" aria-label="cogito-agent">\n<defs>{defs}</defs>\n'
            f'<rect width="{S}" height="{S}" rx="104" fill="url(#bg)"/>\n<g>{echo}</g>\n<g>{main}</g>\n</svg>\n')

def social_svg():
    """GitHub social preview 卡：1280×640，wordmark 置中 + 標語 + 一行描述。"""
    W, H = 1280, 640
    TEXT, cell = "COGITO-AGENT", 14
    lw, lh = wcells(TEXT)*cell, 7*cell
    x0, y0, cx = (W-lw)/2, 200, W/2
    tag_y, sub_y, desc_y = 360, 394, 434
    mono = 'ui-monospace,SFMono-Regular,Menlo,Consolas,monospace'
    defs = (f'<linearGradient id="g" gradientUnits="userSpaceOnUse" x1="0" y1="{y0}" x2="0" y2="{y0+lh}">'
            f'<stop offset="0" stop-color="{G1}"/><stop offset="0.34" stop-color="{G2}"/>'
            f'<stop offset="0.66" stop-color="{G3}"/><stop offset="1" stop-color="{G4}"/></linearGradient>'
            f'<radialGradient id="bg" cx="0.5" cy="0.36" r="0.85">'
            f'<stop offset="0" stop-color="{GROUND2}"/><stop offset="0.66" stop-color="{GROUND}"/></radialGradient>')
    echo = svg_rects(TEXT, cell, x0+4, y0+4, G3, ' fill-opacity="0.12"')
    main = svg_rects(TEXT, cell, x0, y0, "url(#g)")
    desc = "self-hosted ReAct coding agent &#183; Claude-driven &#183; Slack / Telegram"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" '
            f'role="img" aria-label="COGITO-AGENT — cogito, ergo ago">\n<defs>{defs}</defs>\n'
            f'<rect width="{W}" height="{H}" fill="url(#bg)"/>\n<g>{echo}</g>\n<g>{main}</g>\n'
            f'<text x="{cx}" y="{tag_y}" text-anchor="middle" font-family="{mono}" font-size="30" '
            f'font-weight="600" fill="{G2}" letter-spacing="0.5">cogito, ergo ago</text>\n'
            f'<text x="{cx}" y="{sub_y}" text-anchor="middle" font-family="{mono}" font-size="14" '
            f'font-weight="500" fill="{INK_DIM}" letter-spacing="5">REASON &#183; ACT &#183; OBSERVE</text>\n'
            f'<text x="{cx}" y="{desc_y}" text-anchor="middle" font-family="{mono}" font-size="16" '
            f'fill="#b8a493">{desc}</text>\n</svg>\n')

# ---------------- PNG (Pillow) ----------------
def png_pixels(d, text, cell, x0, y0, gy0, glh, color=None, alpha=255):
    col, rad = 0, int(cell*0.14)
    for ch in text:
        g = FONT.get(ch, FONT[' '])
        for r in range(7):
            for c in range(5):
                if g[r][c] == '1':
                    x, y = x0+(col+c)*cell, y0+r*cell
                    fill = (color if color else lerp(((y+cell/2)-gy0)/glh)) + (alpha,)
                    d.rounded_rectangle([x, y, x+cell-1, y+cell-1], radius=rad, fill=fill)
        col += 6

def render_pngs():
    try:
        from PIL import Image, ImageDraw, ImageFont
    except ImportError:
        print("（略過 PNG：未安裝 Pillow —— pip install Pillow）")
        return
    mono = next((p for p in ["/System/Library/Fonts/Menlo.ttc",
                             "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"] if os.path.exists(p)), None)
    def font(sz, bold):
        if not mono: return ImageFont.load_default()
        return ImageFont.truetype(mono, sz, index=(1 if bold and mono.endswith(".ttc") else 0))
    def tracked(d, text, cx, y, fnt, fill, track):  # 置中 + 字距
        ws = [d.textlength(c, font=fnt) for c in text]
        x = cx-(sum(ws)+track*(len(text)-1))/2
        for c, w in zip(text, ws):
            d.text((x, y), c, font=fnt, fill=fill, anchor="lm"); x += w+track
    SC = 3
    # banner
    TEXT, cell = "COGITO-AGENT", 20*SC
    pad_x, pad_top = 78*SC, 74*SC
    lw, lh = wcells(TEXT)*cell, 7*cell
    W = lw+2*pad_x; tag_y = pad_top+lh+52*SC; sub_y = tag_y+34*SC; H = sub_y+40*SC; cx = W//2
    img = Image.new("RGBA",(W,H),(20,17,16,255))
    d = ImageDraw.Draw(img)
    png_pixels(d,TEXT,cell,pad_x+6*SC,pad_top+6*SC,pad_top,lh,color=(232,115,74),alpha=31)
    png_pixels(d,TEXT,cell,pad_x,pad_top,pad_top,lh)
    d.text((cx,tag_y),"cogito, ergo ago",font=font(30*SC,True),fill=(239,154,74,255),anchor="mm")
    tracked(d,"REASON  ·  ACT  ·  OBSERVE",cx,sub_y,font(15*SC,False),(138,117,102,255),5*SC)
    img.convert("RGB").save(os.path.join(HERE,"banner.png"))
    # social preview 卡 1280×640
    W2, H2, cs = 1280*SC, 640*SC, 14*SC
    lw2 = wcells(TEXT)*cs; x2 = (W2-lw2)//2; y2 = 200*SC
    sc_img = Image.new("RGBA",(W2,H2),(20,17,16,255)); sd = ImageDraw.Draw(sc_img)
    png_pixels(sd,TEXT,cs,x2+4*SC,y2+4*SC,y2,7*cs,color=(232,115,74),alpha=31)
    png_pixels(sd,TEXT,cs,x2,y2,y2,7*cs)
    cx2 = W2//2
    sd.text((cx2,360*SC),"cogito, ergo ago",font=font(30*SC,True),fill=(239,154,74,255),anchor="mm")
    tracked(sd,"REASON  ·  ACT  ·  OBSERVE",cx2,394*SC,font(14*SC,False),(138,117,102,255),5*SC)
    sd.text((cx2,434*SC),"self-hosted ReAct coding agent · Claude-driven · Slack / Telegram",
            font=font(16*SC,False),fill=(184,164,147,255),anchor="mm")
    sc_img.convert("RGB").save(os.path.join(HERE,"social.png"))
    # avatar
    S, cell = 512*SC, 11*SC
    top, bot = "COGITO", "AGENT"
    gap = 3*cell; y_top = (S-(7*cell*2+gap))//2; y_bot = y_top+7*cell+gap
    x_top, x_bot = (S-wcells(top)*cell)//2, (S-wcells(bot)*cell)//2
    av = Image.new("RGBA",(S,S),(0,0,0,0))
    mask = Image.new("L",(S,S),0); ImageDraw.Draw(mask).rounded_rectangle([0,0,S-1,S-1],radius=104*SC,fill=255)
    bg = Image.new("RGBA",(S,S),(20,17,16,255))
    ad = ImageDraw.Draw(bg)
    glh = (y_bot+7*cell)-y_top
    png_pixels(ad,top,cell,x_top+4*SC,y_top+4*SC,y_top,glh,color=(232,115,74),alpha=33)
    png_pixels(ad,bot,cell,x_bot+4*SC,y_bot+4*SC,y_top,glh,color=(232,115,74),alpha=33)
    png_pixels(ad,top,cell,x_top,y_top,y_top,glh)
    png_pixels(ad,bot,cell,x_bot,y_bot,y_top,glh)
    av.paste(bg,(0,0),mask)
    av.save(os.path.join(HERE,"avatar.png"))
    print(f"PNG: banner {W}x{H}, avatar {S}x{S}")

if __name__ == "__main__":
    open(os.path.join(HERE,"banner.svg"),"w").write(banner_svg())
    open(os.path.join(HERE,"avatar.svg"),"w").write(avatar_svg())
    open(os.path.join(HERE,"social.svg"),"w").write(social_svg())
    print("SVG: banner.svg + avatar.svg + social.svg")
    render_pngs()
