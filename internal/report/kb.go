package report

import "strings"

// 确定性知识库(第五章 §6 + Q10 初始条目,P2-10 落地):修复建议按
// vulnclass 查预置模板,事实成果类按陈述关键词分类——零 LLM、纯查表
// (R17 同源纪律),面板详情抽屉与报告导出共用本文件,同进同出。
// 未命中的输入不编造:修复建议返回空(读侧如实呈现"无预置建议"),
// 成果类返回空串(读侧归"普通观察")。

// LootClass 成果类(P2-10 事实页签的红队战果清单维度)。
const (
	LootCredential = "credential" // 凭据
	LootData       = "data"       // 数据
	LootPermission = "permission" // 权限
	LootFoothold   = "foothold"   // 立足点
)

// remediationKB 修复建议模板表:Aliases 为可命中的 vulnclass 词形
// (模型自由填写的漏洞类型,取词形并集),Items 为逐条可执行建议。
// 初始条目按 P2 已见 vulnclass 与高频类型收录,随版本维护(Q10)。
// P4-59 ②(问题清单 #80③)全量校准:每条一个具体动作——改哪里、改
// 成什么样;"加强安全防护"式无动作空话、"收敛/复核/加固"类模糊动词
// 不再出现;修复面单一的类型一句,面多的分条(每条仍是一个具体动作);
// kb_test.go 按空话黑名单逐条断言。
var remediationKB = []struct {
	Aliases []string
	Items   []string
}{
	{[]string{"弱口令", "口令复用", "默认口令"}, []string{
		"立即把该账号口令换成 12 位以上的随机口令,不与其他系统共用",
		"开启登录失败锁定与双因素认证",
		"排查全部账号,凡复用该口令的一并更换",
	}},
	{[]string{"未授权访问"}, []string{
		"对该入口增加登录与权限校验,未登录请求一律拒绝",
		"按角色划分接口与页面的访问权限,默认拒绝、显式放行",
		"在网关或路由层拦截未鉴权路径的访问",
	}},
	{[]string{"信息暴露", "敏感信息泄露", "信息泄露"}, []string{
		"从响应、页面与公开文件中删除该敏感内容",
		"关闭目录列表与调试接口",
		"错误响应去掉堆栈、内部路径等细节,只返回状态码与一句说明",
		"已暴露的凭据类内容按泄露处置:立即改密码、换密钥",
	}},
	{[]string{"备份暴露", "备份文件暴露"}, []string{
		"删除线上可直接访问的备份与历史文件",
		"在 Web 服务禁止下载 .bak/.sql/.zip 等备份类扩展名",
		"把备份统一存放到 Web 目录之外的存储区",
		"发布上线前检查产物目录,拦截误带的备份文件",
	}},
	{[]string{"注入", "SQL注入", "SQL 注入", "命令注入"}, []string{
		"把 SQL 查询改为参数化查询或预编译语句,禁止拼接变量",
		"服务端按白名单校验输入的类型与长度",
		"数据库账号去掉文件读写与执行系统命令的权限",
	}},
	{[]string{"XSS", "跨站脚本"}, []string{
		"按输出上下文转义(HTML、属性、JS 分别处理)",
		"设置 Content-Security-Policy,只允许页面自身来源的脚本",
		"会话 Cookie 加上 HttpOnly 与 SameSite",
	}},
	{[]string{"SSRF", "请求伪造"}, []string{
		"服务端发起请求前按白名单校验目标地址",
		"拒绝内网网段与云元数据地址(如 169.254.169.254)",
		"重定向跳转后按白名单重新校验目标",
	}},
	{[]string{"文件上传", "任意文件上传"}, []string{
		"上传类型白名单校验(Content-Type 与扩展名双查),禁止脚本类扩展名",
		"上传文件保存到 Web 根目录之外,文件名改为随机名",
		"上传目录关闭脚本执行权限",
	}},
	{[]string{"越权", "水平越权", "垂直越权", "权限提升"}, []string{
		"服务端按资源属主校验访问,不信任客户端传入的标识",
		"管理面操作增加二次鉴权并记录审计日志",
	}},
	{[]string{"CSRF", "跨站请求伪造"}, []string{
		"状态变更请求校验一次性 CSRF Token",
		"会话 Cookie 设置 SameSite",
		"状态变更请求校验 Origin 与 Referer",
	}},
	{[]string{"目录遍历", "路径遍历"}, []string{
		"文件路径归一化后限制在白名单根目录内",
		"拒绝包含 ../ 与绝对路径的输入",
	}},
	{[]string{"反序列化"}, []string{
		"反序列化输入改用 JSON 等安全格式",
		"保留对象反序列化时按白名单限制可反序列化的类型",
		"升级存在已知反序列化漏洞的组件",
	}},
	{[]string{"命令执行", "RCE", "远程代码执行"}, []string{
		"去掉用户输入拼接系统命令的代码,改用参数化 API",
		"服务进程以最小权限账户运行",
		"禁用语言中不必要的命令执行函数",
	}},
}

// Remediation 按 vulnclass 查表修复建议(R17:确定性查表,零 LLM;
// 面板详情抽屉与报告同源共用)。命中规则:先整词相等,再按声明顺序
// 词形包含(Aliases 词出现在 vulnclass 里,或 vulnclass 本身是别名
// 的更长写法);未命中返回 nil,读侧如实呈现,不造占位建议。
func Remediation(vulnclass string) []string {
	v := strings.TrimSpace(vulnclass)
	if v == "" {
		return nil
	}
	for _, e := range remediationKB {
		for _, alias := range e.Aliases {
			if v == alias || strings.Contains(v, alias) || strings.Contains(alias, v) && len(v) >= 2 {
				return e.Items
			}
		}
	}
	return nil
}

// lootRules 成果类关键词规则:按声明顺序首个命中生效(陈述同时含多类
// 线索时,取战果价值更高的类——凭据先于立足点先于权限先于数据)。
// 取词避开歧义词:「会话」不进凭据(普通会话/会话固定是泛用词,只收
// 英文 session)、「授权」不进权限(几乎总以"未授权"否定形出现)。
var lootRules = []struct {
	Class string
	Words []string
}{
	{LootCredential, []string{"密码", "口令", "凭据", "凭证", "密钥", "私钥", "token", "session", "cookie", "jwt", "apikey", "api key", "账号密码"}},
	{LootFoothold, []string{"webshell", "getshell", "shell", "反弹", "后门", "命令执行", "rce", "持久化", "落脚", "立足"}},
	{LootPermission, []string{"管理员", "越权", "提权", "横向", "权限", "admin", "root"}},
	{LootData, []string{"数据", "记录", "个人信息", "手机号", "身份证", "邮箱", "导出", "泄露"}},
}

// LootClass 事实成果类分类(P2-10 事实页签红队战果清单):按陈述关键词
// 确定性分类,零 LLM;无命中返回空串(普通观察,不属战果清单)。
func LootClass(statement string) string {
	s := strings.ToLower(statement)
	for _, r := range lootRules {
		for _, w := range r.Words {
			if strings.Contains(s, w) {
				return r.Class
			}
		}
	}
	return ""
}
