/* ===========================================================================
   Painel da Aula 1 — TechPix
   Vanilla JS, zero dependência. O painel é só mais um cliente da API pública:
   ele não tem atalho nenhum para dentro do sistema.
   =========================================================================== */

const API = location.origin;
let BACEN = 'http://localhost:9090';

const CHAVES = [
  { v: 'bruno@bancobeta.com.br', l: 'bruno@bancobeta.com.br — Bruno Alves (Banco Beta)' },
  { v: '+5521988887777', l: '+5521988887777 — Bruno Alves (Banco Beta)' },
  { v: '12345678909', l: '12345678909 — CPF de Bruno (Banco Beta)' },
  { v: '3f1b9d5e-7c42-4a1b-9f2d-6e5a4c3b2a10', l: 'EVP aleatória — Marina Costa (Banco Gama)' },
  { v: 'joao@techpix.com.br', l: 'joao@techpix.com.br — João Lima (TechPix)' },
  { v: '39053344705', l: '39053344705 — Carla Dias (TechPix)' },
  { v: 'golpista@fraude.com', l: 'golpista@fraude.com — recebedor em lista PLD-FT ⚠' },
];

let CONTAS = [];          // códigos de carteira de cliente
let ULTIMO_ORCAMENTO = null;

/* ── utilidades ─────────────────────────────────────────── */
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

function el(tag, attrs = {}, ...filhos) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') n.className = v;
    else if (k === 'html') n.innerHTML = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined) n.setAttribute(k, v);
  }
  for (const f of filhos.flat()) {
    if (f === null || f === undefined || f === false) continue;
    n.append(f.nodeType ? f : document.createTextNode(String(f)));
  }
  return n;
}

const brl = (centavos) =>
  (centavos / 100).toLocaleString('pt-BR', { style: 'currency', currency: 'BRL' });
const ms = (v) => (v >= 1000 ? (v / 1000).toFixed(2) + 's' : Math.round(v) + 'ms');
const hora = (d = new Date()) => d.toLocaleTimeString('pt-BR', { hour12: false });
// Com milissegundos: é neles que a corrida entre os toques aparece.
const relogio = (d) => hora(d) + '.' + String(d.getMilliseconds()).padStart(3, '0');

async function pedir(url, opcoes = {}) {
  const t0 = performance.now();
  try {
    const r = await fetch(url, {
      ...opcoes,
      headers: { 'Content-Type': 'application/json', ...(opcoes.headers || {}) },
    });
    const texto = await r.text();
    let corpo = null;
    try { corpo = texto ? JSON.parse(texto) : null; } catch { corpo = { bruto: texto }; }
    return {
      ok: r.ok, status: r.status, corpo, ms: performance.now() - t0,
      cabecalhos: r.headers,
    };
  } catch (e) {
    return { ok: false, status: 0, corpo: { erro: { mensagem: String(e) } }, ms: performance.now() - t0 };
  }
}

const api = {
  get: (p) => pedir(API + p),
  post: (p, corpo, cab) => pedir(API + p, { method: 'POST', body: JSON.stringify(corpo || {}), headers: cab }),
};
const bacen = {
  get: (p) => pedir(BACEN + p),
  post: (p, corpo) => pedir(BACEN + p, { method: 'POST', body: JSON.stringify(corpo || {}) }),
};

function brinde(msg, tipo = '') {
  const n = el('div', { class: 'brinde-item ' + tipo }, msg);
  $('#brinde').append(n);
  setTimeout(() => { n.style.opacity = '0'; setTimeout(() => n.remove(), 300); }, 4200);
}

function kpi(rot, val, classe = '') {
  return el('div', { class: 'kpi' },
    el('div', { class: 'kpi-rot' }, rot),
    el('div', { class: 'kpi-val ' + classe }, val));
}

function tabela(cabecalhos, linhas) {
  const t = el('table', { class: 'tabela' });
  t.append(el('thead', {}, el('tr', {}, cabecalhos.map((c) => el('th', {}, c)))));
  t.append(el('tbody', {}, linhas.length
    ? linhas.map((l) => el('tr', { class: l.classe || '' }, l.celulas))
    : el('tr', {}, el('td', { colspan: cabecalhos.length }, el('div', { class: 'vazio-msg' }, 'nada por aqui ainda')))));
  return t;
}

function trocar(alvo, ...conteudo) {
  const n = typeof alvo === 'string' ? $(alvo) : alvo;
  if (!n) return;
  n.replaceChildren(...conteudo.flat().filter(Boolean));
}

async function novoE2E() {
  const r = await api.post('/v1/pix/e2e');
  return r.corpo?.e2e_id;
}

function encherSelect(sel, itens, valorPadrao) {
  if (!sel) return;
  const atual = sel.value;
  sel.replaceChildren(...itens.map((i) => el('option', { value: i.v }, i.l)));
  const escolhido = [atual, valorPadrao].find((v) => v && itens.some((i) => i.v === v));
  if (escolhido) sel.value = escolhido;
}

/* ── navegação ──────────────────────────────────────────── */
let SECAO_ATUAL = null;
let TIMER = null;

function irPara(nome) {
  if (!SECOES[nome]) nome = 'visao';
  SECAO_ATUAL = nome;

  $$('#nav a').forEach((a) => a.classList.toggle('ativo', a.dataset.secao === nome));
  $$('.secao').forEach((s) => s.classList.toggle('ativa', s.id === 'secao-' + nome));

  clearInterval(TIMER);
  const s = SECOES[nome];
  if (s.montar && !s.montada) { s.montar(); s.montada = true; }
  if (s.atualizar) {
    s.atualizar();
    if (s.intervalo) TIMER = setInterval(() => s.atualizar(), s.intervalo);
  }
}

window.addEventListener('hashchange', () => irPara(location.hash.slice(1)));

/* ===========================================================================
   VISÃO GERAL
   =========================================================================== */
const visao = {
  intervalo: 2000,
  montar() {
    $('#btn-depositar').addEventListener('click', async (ev) => {
      const b = ev.currentTarget;
      const conta = $('#dep-conta').value;
      const centavos = Math.round(parseFloat($('#dep-valor').value || '0') * 100);
      if (centavos <= 0) return brinde('valor precisa ser positivo', 'erro');
      b.disabled = true;
      const r = await api.post(`/v1/contas/${conta}/depositos`, { valor_centavos: centavos, descricao: 'painel da aula' },
        { 'Idempotency-Key': 'painel-' + Date.now() + '-' + Math.random().toString(36).slice(2) });
      b.disabled = false;
      r.ok ? brinde(`cash-in de ${brl(centavos)} em ${conta}`, 'ok')
           : brinde('depósito recusado: ' + (r.corpo?.erro?.mensagem || r.status), 'erro');
      visao.atualizar();
    });
  },
  async atualizar() {
    const [contas, fit, proj, clientes, pags] = await Promise.all([
      api.get('/v1/ledger/contas'), api.get('/v1/fitness'),
      api.get('/v1/projecao'), api.get('/v1/contas'), api.get('/v1/pix/pagamentos?limite=8'),
    ]);
    if (!contas.ok) return;

    const cs = contas.corpo.contas || [];
    const ativo = cs.filter((c) => c.natureza === 'ATIVO').reduce((s, c) => s + c.saldo_centavos, 0);
    const passivo = cs.filter((c) => c.natureza === 'PASSIVO').reduce((s, c) => s + c.saldo_centavos, 0);

    trocar('#balanco',
      el('div', { class: 'balanco-lado' },
        el('div', { class: 'balanco-rot' }, 'Ativo — o que temos no BACEN'),
        el('div', { class: 'balanco-val' }, brl(ativo))),
      el('div', { class: 'balanco-igual' }, ativo === passivo ? '=' : '≠'),
      el('div', { class: 'balanco-lado' },
        el('div', { class: 'balanco-rot' }, 'Passivo — o que devemos'),
        el('div', { class: 'balanco-val' }, brl(passivo))),
      el('span', { class: 'tag ' + (ativo === passivo ? 'ok' : 'perigo') },
        ativo === passivo ? 'CONSERVADO' : 'DIVERGENTE'));

    if (fit.ok) {
      trocar('#fitness-resumo', (fit.corpo.checks || []).map((c) =>
        el('div', { class: 'check-mini' },
          el('b', { class: 'tag ' + (c.aprovado ? 'ok' : 'perigo') }, c.aprovado ? 'OK' : 'FALHOU'),
          c.nome.replace(/_/g, ' '))));
    }

    if (proj.ok) {
      trocar('#projecao-resumo',
        kpi('não projetados', proj.corpo.lancamentos_nao_projetados,
          proj.corpo.lancamentos_nao_projetados > 0 ? 'aviso' : 'ok'),
        kpi('atraso proposital', proj.corpo.atraso_proposital_ms + 'ms', 'eventual'),
        kpi('tique', proj.corpo.intervalo_ms + 'ms', 'pequeno'));
    }

    if (clientes.ok) {
      const cl = clientes.corpo.clientes || [];
      CONTAS = cl.map((c) => ({ v: c.conta, l: `${c.conta} — ${c.titular}` }));
      $$('#dep-conta, #tq-conta, #cs-conta, #spi-conta, #lg-conta')
        .forEach((s) => encherSelect(s, CONTAS, 'carteira:ana'));
      // A demo de contenção começa na Carla: conta enxuta, para o limite de
      // saldo aparecer de verdade em vez de todos os pagamentos passarem.
      encherSelect($('#ct-conta'), CONTAS, 'carteira:carla');
      encherSelect($('#lg-conta'), [...CONTAS, { v: 'pix_a_liquidar', l: 'pix_a_liquidar (transitória)' },
        { v: 'reserva_no_bc', l: 'reserva_no_bc (Conta PI)' }], 'carteira:ana');

      trocar('#clientes', cl.map((c) => el('div', { class: 'cliente' },
        el('div', {},
          el('div', { class: 'cliente-nome' }, c.titular),
          el('div', { class: 'cliente-chave' }, (c.chaves_pix || []).join(' · ') || 'sem chave')),
        el('div', { class: 'cliente-saldo' }, c.saldo))));
    }

    trocar('#tabela-contas', tabela(['conta', 'natureza', 'saldo'], cs.map((c) => ({
      celulas: [
        el('td', {}, el('code', {}, c.codigo)),
        el('td', {}, el('span', { class: 'tag ' + c.natureza.toLowerCase() }, c.natureza)),
        el('td', { class: 'num' }, c.saldo),
      ],
    }))));

    if (pags.ok) {
      trocar('#tabela-pagamentos', tabela(['e2e id', 'para', 'valor', 'status', 'spi'],
        (pags.corpo.pagamentos || []).map((p) => ({
          celulas: [
            el('td', {}, el('code', {}, p.e2e_id.slice(0, 14) + '…')),
            el('td', {}, p.nome_recebedor || p.chave_recebedor),
            el('td', { class: 'num' }, p.valor),
            el('td', {}, el('span', { class: 'tag ' + selo(p.status) }, p.status)),
            el('td', {}, p.status_spi || '—'),
          ],
        }))));
    }
  },
};

const selo = (s) => ({ SETTLED: 'ok', RESERVED: 'aviso', REFUNDED: 'perigo' }[s] || 'neutro');

// O motivo específico (LIMITE_NOTURNO, PLDFT_BLOQUEADO…) mora em detalhes;
// o código de topo é só a família da recusa.
const motivoDe = (r) => r.corpo?.erro?.detalhes?.motivo || r.corpo?.erro?.codigo || '—';

// Recusas que têm conserto óbvio ganham a dica junto — em aula, adivinhar
// o parâmetro errado custa cinco minutos de plateia parada.
const DICAS = {
  LIMITE_NOTURNO: 'Entre 20h e 6h (horário de Brasília) o limite é R$ 1.000. Baixe o valor, ' +
    'ou suba PIX_NIGHT_LIMIT_CENTS no docker-compose.yml e rode `docker compose up -d techpix`.',
  SALDO_INSUFICIENTE: 'Volte à Visão geral e faça um cash-in, ou baixe o valor.',
  LIMITE_EXCEDIDO: 'Acima do teto por transação (PIX_MAX_AMOUNT_CENTS).',
  PLDFT_BLOQUEADO: 'Recebedor em lista restritiva — falhar fechado é o comportamento correto aqui.',
};

/* ===========================================================================
   OS TRÊS TOQUES
   =========================================================================== */
const toques = {
  montar() {
    encherSelect($('#tq-chave'), CHAVES, 'bruno@bancobeta.com.br');
    $('#btn-toques').addEventListener('click', () => toques.disparar(3));
    $('#btn-toque-1').addEventListener('click', () => toques.disparar(1));
    $('#tq-mesma-chave').addEventListener('change', toques.avisoChave);
    toques.avisoChave();
  },
  atualizar() { encherSelect($('#tq-chave'), CHAVES, 'bruno@bancobeta.com.br'); },

  avisoChave() {
    const mesma = $('#tq-mesma-chave').checked;
    $('#tq-aviso-chave').textContent = mesma
      ? 'Os três toques carregam o MESMO EndToEndId: é a mesma intenção, repetida pela rede.'
      : '⚠ Cada toque com chave própria = três intenções DIFERENTES. O sistema vai debitar três vezes — e estará certo.';
  },

  async disparar(n) {
    const btn = $('#btn-toques'); btn.disabled = true;
    const conta = $('#tq-conta').value;
    const chave = $('#tq-chave').value;
    const centavos = Math.round(parseFloat($('#tq-valor').value || '0') * 100);
    const mesma = $('#tq-mesma-chave').checked;

    const antes = (await api.get(`/v1/contas/${conta}/saldo`)).corpo?.forte?.saldo_centavos ?? 0;

    const chaves = mesma
      ? Array(n).fill(await novoE2E())
      : await Promise.all(Array.from({ length: n }, () => novoE2E()));
    $('#tq-e2e').textContent = mesma ? 'EndToEndId: ' + chaves[0] : n + ' chaves distintas';

    trocar('#tq-cartoes', chaves.map((_, i) =>
      el('div', { class: 'toque pendente', id: 'toque-' + i },
        el('div', { class: 'toque-topo' },
          el('span', { class: 'toque-num' }, 'toque ' + (i + 1)),
          el('span', { class: 'toque-http' }, '…')),
        el('div', { class: 'toque-corpo' }, 'enviando…'))));
    trocar('#tq-efeito'); trocar('#tq-veredito'); trocar('#tq-ledger');

    const resultados = await Promise.all(chaves.map(async (e2e, i) => {
      const r = await api.post('/v1/pix/pagamentos', {
        e2e_id: e2e, conta_pagador: conta, chave_recebedor: chave,
        valor_centavos: centavos, descricao: 'Black Friday',
      });
      // Instante em que ESTA resposta chegou. Os três são diferentes — e é
      // justamente esse contraste com o instante único do fato que interessa.
      const respondidoEm = new Date();
      toques.pintar(i, r, respondidoEm);
      return { ...r, e2e, respondidoEm };
    }));

    ULTIMO_ORCAMENTO = resultados.find((r) => r.corpo?.orcamento_latencia)?.corpo.orcamento_latencia || ULTIMO_ORCAMENTO;
    await toques.veredito(conta, antes, resultados, mesma, centavos, n);
    btn.disabled = false;
  },

  pintar(i, r, respondidoEm) {
    const p = r.corpo?.pagamento;
    const replay = r.corpo?.replay === true;
    const erro = !r.ok;
    const classe = erro ? 'erro' : replay ? 'replay' : 'executou';
    const rotulo = erro ? 'RECUSADO' : replay ? 'REPLAY — nenhum débito novo' : 'EXECUTOU — debitou uma vez';

    trocar('#toque-' + i,
      el('div', { class: 'toque-topo' },
        el('span', { class: 'toque-num' }, 'toque ' + (i + 1)),
        el('span', { class: 'toque-http' }, r.status)),
      el('div', { class: 'tag ' + (erro ? 'perigo' : replay ? 'aviso' : 'ok') }, rotulo),
      el('div', { class: 'toque-linha' }, el('span', {}, 'respondido às'), el('span', {}, relogio(respondidoEm))),
      el('div', { class: 'toque-linha' }, el('span', {}, 'levou'), el('span', {}, ms(r.ms))),
      p ? el('div', { class: 'toque-linha' }, el('span', {}, 'status'), el('span', {}, p.status)) : null,
      p ? el('div', { class: 'toque-linha' }, el('span', {}, 'tx reserva'), el('span', {}, (p.tx_reserva || '—').slice(0, 8))) : null,
      // Numa recusa, o que ensina é o motivo ESPECÍFICO — não o código guarda-chuva.
      erro ? el('div', { class: 'toque-linha' }, el('span', {}, 'motivo'), el('span', {}, motivoDe(r))) : null,
      erro ? el('div', { class: 'toque-detalhe' }, r.corpo?.erro?.mensagem || '') : null,
      el('div', { class: 'toque-barra', style: `width:${Math.min(100, (r.ms / 1200) * 100)}%` }));
    $('#toque-' + i).className = 'toque ' + classe;
  },

  async veredito(conta, antes, resultados, mesma, centavos, n) {
    const depois = (await api.get(`/v1/contas/${conta}/saldo`)).corpo?.forte?.saldo_centavos ?? 0;
    const movido = antes - depois;
    const aceitos = resultados.filter((r) => r.ok);
    const recusados = resultados.filter((r) => !r.ok);
    const execucoes = resultados.filter((r) => r.ok && r.corpo?.replay === false).length;
    const replays = resultados.filter((r) => r.corpo?.replay === true).length;

    // Quanto DEVERIA ter saído: uma recusa move zero, e isso é o certo.
    const esperado = recusados.length === resultados.length ? 0 : (mesma ? centavos : centavos * aceitos.length);
    const certo = movido === esperado;

    trocar('#tq-efeito',
      kpi('saldo antes', brl(antes)),
      kpi('saldo depois', brl(depois), 'forte'),
      kpi('saiu da conta', brl(movido), certo ? 'ok' : 'perigo'),
      kpi('execuções', execucoes, 'ok'),
      kpi('replays', replays, 'eventual'),
      recusados.length ? kpi('recusas', recusados.length, 'aviso') : null);

    const v = el('div', { class: 'veredito' + (certo ? '' : ' ruim') });
    const motivo = recusados.length ? motivoDe(recusados[0]) : null;
    const mensagem = recusados[0]?.corpo?.erro?.mensagem || '';
    const dica = DICAS[motivo] ? `<br><span class="mini">${DICAS[motivo]}</span>` : '';
    // Recusa ANTES do ledger (risco, chave inválida) nem chega a tocar na chave
    // de idempotência: não moveu nada, então pode ser reavaliada à vontade.
    const antesDoLedger = ['LIMITE_NOTURNO', 'LIMITE_EXCEDIDO', 'PLDFT_BLOQUEADO', 'VALOR_INVALIDO'].includes(motivo);

    if (!certo) {
      v.innerHTML = `Movimentou ${brl(movido)} quando deveria ter movimentado ${brl(esperado)}.
        <strong>Invariante violada</strong> — isso é incidente, não resultado.`;
    } else if (recusados.length === resultados.length) {
      v.innerHTML = `Os ${n} toques foram <strong>recusados</strong> — <code>${motivo}</code> —
        e o saldo <strong>não se moveu</strong>.<br>${mensagem}<br>` +
        (antesDoLedger
          ? `Esta recusa acontece <strong>antes</strong> do ledger, então nem chegou a usar a chave
             de idempotência: nada foi gravado, e por isso ela pode ser reavaliada a qualquer momento
             (a regra de hoje pode não ser a de amanhã).`
          : `Repare que as ${n} respostas são idênticas: a recusa também é idempotente. A primeira
             tentativa decidiu, as outras receberam a mesma decisão de volta — não uma segunda
             chance de passar.`) + dica;
    } else if (recusados.length) {
      v.innerHTML = `${aceitos.length} aceito(s) e ${recusados.length} recusado(s)
        (<code>${motivo}</code>). Saiu da conta ${brl(movido)} — exatamente o que foi aceito.${dica}`;
    } else if (mesma) {
      v.innerHTML = `Ana tocou <strong>${n}×</strong>. Aconteceu <strong>1×</strong>.
        Foi respondida <strong>${n}×</strong>.<br>
        Saiu da conta exatamente ${brl(centavos)} — o valor de <em>um</em> pagamento.`;
    } else {
      v.innerHTML = `Foram <strong>${n} intenções diferentes</strong>, então houve ${n} débitos: ${brl(movido)}.
        Isso está <strong>correto</strong> — idempotência não é "bloquear repetição",
        é "honrar cada intenção uma vez".`;
    }
    trocar('#tq-veredito', v);

    const e2e = resultados[0].e2e;
    const lg = await api.get('/v1/ledger/e2e/' + e2e);
    const txs = lg.corpo?.transacoes || [];

    // O contraste que dá nome à aula: N respostas em N instantes distintos,
    // um único fato econômico num instante só.
    const instantes = resultados.map((r) => r.respondidoEm).sort((a, b) => a - b);
    const espalhamento = instantes.at(-1) - instantes[0];
    const reserva = txs.find((t) => t.tipo === 'pix_reserva');

    trocar('#tq-ledger',
      el('div', { class: 'contraste' },
        el('div', {},
          el('span', { class: 'contraste-rot' }, `${n} respostas, instantes diferentes`),
          el('span', { class: 'contraste-val' }, instantes.map(relogio).join('  ·  ')),
          el('span', { class: 'mini' }, `espalhadas em ${Math.round(espalhamento)}ms`)),
        reserva ? el('div', {},
          el('span', { class: 'contraste-rot' }, '1 fato econômico, instante único'),
          el('span', { class: 'contraste-val forte' }, relogio(new Date(reserva.ocorrido_em))),
          el('span', { class: 'mini' }, 'gravado uma vez, imutável para sempre')) : null),
      txs.length ? txs.map((t) => cartaoTransacao(t))
        : el('div', { class: 'vazio-msg' }, 'nenhum lançamento — o dinheiro não se moveu, como manda a recusa'));
  },
};

/* Bloco visual de partida dobrada. */
function cartaoTransacao(t, nova = false) {
  const deb = t.lancamentos.filter((l) => l.direcao === 'D');
  const cred = t.lancamentos.filter((l) => l.direcao === 'C');
  const soma = (ls) => ls.reduce((s, l) => s + l.valor_centavos, 0);
  const classe = t.tipo.includes('estorno') ? 'estorno' : t.tipo.includes('liquidacao') ? 'liquidacao' : '';

  const coluna = (rot, ls, tipo) => el('div', { class: 'partida-col ' + tipo },
    el('div', { class: 'partida-rot' }, rot),
    ls.map((l) => el('div', { class: 'partida-item' },
      el('code', {}, l.conta), el('b', {}, l.valor))));

  return el('div', { class: `transacao ${classe} ${nova ? 'nova' : ''}`, 'data-id': t.id },
    el('div', { class: 'transacao-topo' },
      el('div', {},
        el('span', { class: 'transacao-tipo' }, t.tipo),
        el('div', { class: 'transacao-meta' }, t.descricao || '')),
      el('div', { class: 'transacao-meta' },
        new Date(t.ocorrido_em).toLocaleTimeString('pt-BR', { hour12: false }),
        t.e2e_id ? ' · ' + t.e2e_id.slice(0, 12) + '…' : '')),
    el('div', { class: 'partida' },
      coluna('Débito', deb, 'deb'),
      coluna('Crédito', cred, 'cred'),
      el('div', { class: 'partida-soma' },
        `Σ ${brl(soma(deb))} = Σ ${brl(soma(cred))} ✓`)));
}

/* ===========================================================================
   LEDGER AO VIVO
   =========================================================================== */
const ledger = {
  intervalo: 1200,
  vistos: new Set(),
  montar() {
    $('#lg-conta').addEventListener('change', () => ledger.atualizar());
  },
  async atualizar() {
    if (!$('#lg-auto').checked && ledger.vistos.size) return;
    const [txs, contas] = await Promise.all([
      api.get('/v1/ledger/transacoes?limite=25'),
      api.get('/v1/ledger/contas'),
    ]);
    if (!txs.ok) return;
    const lista = txs.corpo.transacoes || [];

    trocar('#feed-ledger', lista.map((t) => {
      const nova = ledger.vistos.size > 0 && !ledger.vistos.has(t.id);
      return cartaoTransacao(t, nova);
    }));
    lista.forEach((t) => ledger.vistos.add(t.id));

    // Conta T: movimentos recentes + saldo real (que vem de TODO o log).
    const codigo = $('#lg-conta').value || 'carteira:ana';
    const conta = (contas.corpo?.contas || []).find((c) => c.codigo === codigo);
    const movs = [];
    lista.forEach((t) => t.lancamentos.forEach((l) => {
      if (l.conta === codigo) movs.push({ ...l, tipo: t.tipo });
    }));
    const deb = movs.filter((m) => m.direcao === 'D');
    const cred = movs.filter((m) => m.direcao === 'C');
    const soma = (ls) => ls.reduce((s, l) => s + l.valor_centavos, 0);

    trocar('#conta-t', el('div', { class: 'conta-t' },
      el('div', { class: 'conta-t-cab' },
        el('strong', {}, codigo),
        el('span', { class: 'tag ' + (conta?.natureza || '').toLowerCase() }, conta?.natureza || '')),
      el('div', { class: 'conta-t-corpo' },
        el('div', { class: 'conta-t-lado deb' },
          el('div', { class: 'conta-t-rot' }, 'Débito'),
          deb.length ? deb.slice(0, 8).map((m) => el('div', { class: 'conta-t-item' },
            el('span', {}, m.tipo), el('span', {}, m.valor))) : el('div', { class: 'mini' }, '—')),
        el('div', { class: 'conta-t-lado cred' },
          el('div', { class: 'conta-t-rot' }, 'Crédito'),
          cred.length ? cred.slice(0, 8).map((m) => el('div', { class: 'conta-t-item' },
            el('span', {}, m.tipo), el('span', {}, m.valor))) : el('div', { class: 'mini' }, '—'))),
      el('div', { class: 'conta-t-rodape' },
        el('span', { class: 'mini' }, `recentes: D ${brl(soma(deb))} · C ${brl(soma(cred))}`),
        el('strong', {}, 'saldo (todo o log): ' + (conta?.saldo || '—')))));
  },
};

/* ===========================================================================
   FORTE vs EVENTUAL
   =========================================================================== */
const consistencia = {
  serie: [],
  montar() {
    $('#btn-consistencia').addEventListener('click', () => consistencia.disparar());
    consistencia.desenhar();
  },
  atualizar() { encherSelect($('#cs-conta'), CONTAS, 'carteira:ana'); },

  async disparar() {
    const btn = $('#btn-consistencia'); btn.disabled = true;
    const conta = $('#cs-conta').value;
    const centavos = Math.round(parseFloat($('#cs-valor').value || '0') * 100);
    consistencia.serie = [];

    // O pagamento roda SEM await: a leitura começa no mesmo instante. Se
    // esperássemos a resposta (que só volta depois do SPI), a projeção já
    // teria alcançado o log e a janela de divergência passaria batido.
    const e2e = await novoE2E();
    api.post('/v1/pix/pagamentos', {
      e2e_id: e2e, conta_pagador: conta, chave_recebedor: 'bruno@bancobeta.com.br',
      valor_centavos: centavos, descricao: 'demo de consistência',
    }).then((r) => {
      if (r.corpo?.orcamento_latencia) ULTIMO_ORCAMENTO = r.corpo.orcamento_latencia;
      if (!r.ok) brinde('pagamento recusado: ' + (r.corpo?.erro?.codigo || r.status), 'erro');
    });

    const inicio = performance.now();
    for (let i = 0; i < 40; i++) {
      const r = await api.get(`/v1/contas/${conta}/saldo`);
      if (r.ok) {
        consistencia.serie.push({
          t: performance.now() - inicio,
          forte: r.corpo.forte.saldo_centavos,
          eventual: r.corpo.eventual.saldo_centavos,
          div: r.corpo.divergencia_centavos,
          naoProj: r.corpo.lancamentos_nao_projetados,
        });
        consistencia.desenhar();
        consistencia.kpis(r.corpo);
      }
      await new Promise((s) => setTimeout(s, 50));
    }
    const ext = await api.get(`/v1/contas/${conta}/extrato?limite=6`);
    trocar('#tabela-extrato', tabela(['tipo', 'dir', 'valor', 'saldo após'],
      (ext.corpo?.linhas || []).map((l) => ({
        celulas: [
          el('td', {}, l.tipo),
          el('td', {}, el('span', { class: 'tag ' + l.direcao.toLowerCase() }, l.direcao)),
          el('td', { class: 'num' }, l.valor),
          el('td', { class: 'num' }, l.saldo_apos),
        ],
      }))));
    btn.disabled = false;
  },

  kpis(d) {
    const divergindo = d.divergencia_centavos !== 0;
    trocar('#cs-kpis',
      kpi('forte (ledger)', d.forte.saldo, 'forte'),
      kpi('eventual (projeção)', d.eventual.saldo, 'eventual'),
      kpi('divergência', brl(d.divergencia_centavos), divergindo ? 'aviso' : 'ok'),
      kpi('não projetados', d.lancamentos_nao_projetados, d.lancamentos_nao_projetados ? 'aviso' : 'ok'));
  },

  desenhar() {
    const c = $('#grafico-consistencia');
    if (!c) return;
    const dpr = window.devicePixelRatio || 1;
    const L = c.clientWidth, A = 240;
    c.width = L * dpr; c.height = A * dpr;
    const g = c.getContext('2d');
    g.scale(dpr, dpr);
    g.clearRect(0, 0, L, A);

    const s = consistencia.serie;
    const css = getComputedStyle(document.documentElement);
    const cor = (n) => css.getPropertyValue(n).trim();

    if (s.length < 2) {
      g.fillStyle = cor('--texto-3'); g.font = '13px sans-serif'; g.textAlign = 'center';
      g.fillText('dispare um Pix para ver a borda ficar para trás', L / 2, A / 2);
      return;
    }

    const vals = s.flatMap((p) => [p.forte, p.eventual]);
    let min = Math.min(...vals), max = Math.max(...vals);
    const folga = Math.max((max - min) * 0.25, 1000);
    min -= folga; max += folga;

    const px = (i) => 46 + (i / (s.length - 1)) * (L - 62);
    const py = (v) => A - 26 - ((v - min) / (max - min || 1)) * (A - 52);

    // faixa de divergência
    g.fillStyle = 'rgba(255,196,77,.22)';
    s.forEach((p, i) => {
      if (!p.div) return;
      const largura = (L - 62) / (s.length - 1);
      g.fillRect(px(i) - largura / 2, 14, largura, A - 40);
    });

    const linha = (campo, corLinha) => {
      g.beginPath(); g.strokeStyle = corLinha; g.lineWidth = 2.5;
      s.forEach((p, i) => (i ? g.lineTo(px(i), py(p[campo])) : g.moveTo(px(i), py(p[campo]))));
      g.stroke();
    };
    linha('eventual', cor('--eventual'));
    linha('forte', cor('--forte'));

    g.fillStyle = cor('--texto-3'); g.font = '11px ui-monospace, monospace';
    g.textAlign = 'left'; g.fillText(brl(max), 6, 18);
    g.fillText(brl(min), 6, A - 8);
    g.textAlign = 'right';
    g.fillText(Math.round(s[s.length - 1].t) + 'ms', L - 6, A - 8);

    const ultimoDiv = s.findLastIndex((p) => p.div !== 0);
    if (ultimoDiv > 0) {
      g.fillStyle = cor('--aviso'); g.textAlign = 'center';
      g.fillText('borda alcançou o log em ~' + Math.round(s[ultimoDiv].t) + 'ms', L / 2, 14);
    }
  },
};

/* ===========================================================================
   DICT
   =========================================================================== */
const dict = {
  intervalo: 1500,
  montar() {
    $$('[data-dict]').forEach((b) => b.addEventListener('click', () => {
      const c = b.dataset.dict;
      dict.consultar(c === '__inexistente__' ? `fantasma${Math.floor(Math.random() * 1e6)}@naoexiste.com` : c);
    }));
    $('#btn-varredura').addEventListener('click', async () => {
      const b = $('#btn-varredura'); b.disabled = true;
      for (let i = 0; i < 5; i++) {
        await dict.consultar(`varredura${Date.now()}${i}@naoexiste.com`);
      }
      b.disabled = false;
    });
    $('#btn-balde-reset').addEventListener('click', async () => {
      await bacen.post('/admin/config', { zerar_baldes: true });
      brinde('baldes reiniciados no BACEN', 'ok');
      dict.atualizar();
    });
  },

  async consultar(chave) {
    const r = await api.get('/v1/bacen/dict/' + encodeURIComponent(chave));
    const c = r.corpo?.chave;
    let custo = '20 tokens', classe = 'perigo', msg;

    if (r.status === 200 && c?.do_cache) { custo = '0 · cache'; classe = 'ok'; msg = `${chave} → ${c.titular} (${c.instituicao}) · sem tocar no BACEN`; }
    else if (r.status === 200) { custo = '1 token'; classe = 'ok'; msg = `${chave} → ${c.titular} (${c.instituicao})`; }
    else if (r.status === 400) { custo = '0 · bloqueio local'; classe = 'aviso'; msg = `${chave} → ${r.corpo?.erro?.mensagem}`; }
    else if (r.status === 404) { msg = `${chave} → não existe no DICT`; }
    else if (r.status === 429) { custo = 'balde vazio'; msg = `${chave} → HTTP 429: rate limit do BACEN`; }
    else { custo = 'circuito'; msg = `${chave} → HTTP ${r.status}: ${r.corpo?.erro?.codigo || ''}`; }

    $('#dict-log').prepend(el('div', { class: 'log-linha ' + classe },
      el('span', { class: 'log-hora' }, hora()),
      el('span', { class: 'log-msg' }, msg),
      el('span', { class: 'log-custo' }, custo),
      el('span', { class: 'log-hora' }, ms(r.ms))));
    dict.atualizar();
  },

  async atualizar() {
    const [estado, baldes] = await Promise.all([api.get('/v1/bacen/estado'), bacen.get('/dict/v1/baldes')]);

    if (baldes.ok) {
      const b = baldes.corpo.baldes?.['00000001'];
      const cap = b?.capacidade ?? 100;
      const tk = b?.tokens_disponiveis ?? cap;
      const pct = Math.max(0, Math.min(100, (tk / cap) * 100));
      const nivel = $('#balde-nivel');
      nivel.style.width = pct + '%';
      nivel.className = pct < 5 ? 'vazio' : pct < 30 ? 'baixo' : '';
      $('#balde-tokens').textContent = Math.round(tk * 10) / 10;
      $('#balde-cap').textContent = cap;
      trocar('#dict-kpis',
        kpi('consumidos', b?.tokens_consumidos ?? 0),
        kpi('bloqueios 429', b?.bloqueios_429 ?? 0, (b?.bloqueios_429 ?? 0) > 0 ? 'perigo' : ''),
        kpi('reposição', (b?.reposicao_por_min ?? 60) + '/min', 'pequeno'),
        kpi('custo de um 404', '20×', 'aviso'));
    }

    if (estado.ok) {
      const d = estado.corpo.dict;
      const est = d.circuito.estado;
      trocar('#breaker',
        ...['fechado', 'meio_aberto', 'aberto'].map((e) =>
          el('span', { class: 'luz ' + (e === est ? 'acesa ' + e : '') })),
        el('span', { class: 'semaforo-txt' }, est.replace('_', '-')),
        el('span', { class: 'mini' }, `${d.circuito.falhas_seguidas}/${d.circuito.limite_falhas} falhas seguidas`));
      trocar('#cache-kpis',
        kpi('cache hits', d.cache_hits, 'ok'),
        kpi('misses', d.cache_misses),
        kpi('bloqueios locais', d.bloqueios_locais, 'aviso'),
        kpi('chaves em cache', d.cache_entradas, 'pequeno'));
    }
  },
};

/* ===========================================================================
   SPI E RECONCILIAÇÃO
   =========================================================================== */
const spi = {
  intervalo: 3000,
  montar() {
    encherSelect($('#spi-chave'), CHAVES, 'bruno@bancobeta.com.br');
    $('#spi-rjct').addEventListener('input', (e) => {
      $('#spi-rjct-val').textContent = Math.round(e.target.value * 100) + '%';
    });
    $('#btn-spi-aplicar').addEventListener('click', spi.aplicar);
    $('#btn-spi-real').addEventListener('click', () => {
      $('#spi-p50').value = 2800; $('#spi-p99').value = 4600; spi.aplicar();
    });
    $('#btn-spi-pagar').addEventListener('click', spi.pagar);
  },

  async aplicar() {
    const r = await bacen.post('/admin/config', {
      spi_p50_ms: +$('#spi-p50').value,
      spi_p99_ms: +$('#spi-p99').value,
      spi_taxa_rejeicao: +$('#spi-rjct').value,
      spi_taxa_buraco_negro: $('#spi-buraco').checked ? 1 : 0,
    });
    r.ok ? brinde('configuração aplicada no simulador do BACEN', 'ok') : brinde('BACEN não respondeu', 'erro');
    spi.atualizar();
  },

  async atualizar() {
    const r = await bacen.get('/admin/config');
    if (!r.ok) return;
    const c = r.corpo;
    trocar('#spi-config',
      kpi('p50', c.spi_p50_ms + 'ms', 'pequeno'),
      kpi('p99', c.spi_p99_ms + 'ms', 'pequeno'),
      kpi('rejeição', Math.round(c.spi_taxa_rejeicao * 100) + '%', c.spi_taxa_rejeicao ? 'aviso' : ''),
      kpi('buraco negro', c.spi_taxa_buraco_negro ? 'LIGADO' : 'desligado', c.spi_taxa_buraco_negro ? 'perigo' : ''),
      kpi('liquidados no SPI', c.pagamentos_liquidados, 'pequeno'));
  },

  async pagar() {
    const btn = $('#btn-spi-pagar'); btn.disabled = true;
    const conta = $('#spi-conta').value;
    const chave = $('#spi-chave').value;
    const centavos = Math.round(parseFloat($('#spi-valor').value || '0') * 100);
    const e2e = await novoE2E();

    const linhas = [];
    const empurrar = (marca, titulo, det, classe = '') => {
      linhas.push({ marca, titulo, det, classe });
      trocar('#spi-timeline', linhas.map((l) => el('div', { class: 'tl-item' },
        el('div', { class: 'tl-marca' }, l.marca),
        el('div', { class: 'tl-corpo ' + l.classe },
          el('div', { class: 'tl-titulo' }, l.titulo),
          l.det ? el('div', { class: 'tl-det' }, l.det) : null))));
    };
    trocar('#spi-janela');
    empurrar('t+0', 'Ordem recebida', `${brl(centavos)} para ${chave} · E2E ${e2e.slice(0, 16)}…`);

    const t0 = performance.now();
    const r = await api.post('/v1/pix/pagamentos', {
      e2e_id: e2e, conta_pagador: conta, chave_recebedor: chave,
      valor_centavos: centavos, descricao: 'painel da aula',
    });
    const orc = r.corpo?.orcamento_latencia;
    if (orc) {
      ULTIMO_ORCAMENTO = orc;
      let acc = 0;
      for (const p of orc.passos) {
        acc += p.ms;
        const externo = p.nome.includes('dict') || p.nome.includes('spi');
        empurrar('t+' + ms(acc), p.nome.replace(/^\d_/, '').replace(/_/g, ' '),
          externo ? 'sistema externo — fora do nosso controle' : 'dentro de casa', externo ? 'externo' : '');
      }
    }

    const p = r.corpo?.pagamento;
    if (!r.ok) {
      empurrar('t+' + ms(r.ms), 'Recusado: ' + (r.corpo?.erro?.codigo || r.status), r.corpo?.erro?.mensagem, 'alerta');
      btn.disabled = false; return;
    }

    if (r.status === 202) {
      empurrar('t+' + ms(r.ms), 'HTTP 202 — desfecho DESCONHECIDO',
        'o SPI não respondeu a tempo. Não estornamos (destruiria dinheiro já liquidado) nem reenviamos (duplicaria).', 'alerta');
      empurrar('', 'Pagamento em RESERVED', 'o dinheiro está em pix_a_liquidar, num limbo com nome. A reconciliação assume daqui.', 'alerta');
    } else {
      empurrar('t+' + ms(r.ms), 'HTTP ' + r.status + ' — ' + p.status,
        p.status_spi ? 'pacs.002 = ' + p.status_spi + (p.motivo_spi ? ' · ' + p.motivo_spi : '') : '', 'ok');
    }

    trocar('#spi-status',
      kpi('status', p.status, selo(p.status) === 'ok' ? 'ok' : 'aviso'),
      kpi('resposta em', ms(r.ms)),
      kpi('http', r.status));

    // Acompanha até o desfecho final e mede a JANELA DE INCERTEZA:
    // o intervalo entre o SPI ter liquidado e nós termos ficado sabendo.
    if (p.status === 'RESERVED') {
      const spiInfo = await api.get('/v1/bacen/spi/' + e2e);
      if (spiInfo.ok) {
        empurrar('', '⚠ O SPI JÁ tinha liquidado',
          'liquidado_em ' + new Date(spiInfo.corpo.liquidado_em).toLocaleTimeString('pt-BR', { hour12: false }) +
          ' — a verdade sempre existiu lá; só não tinha chegado até nós.', 'alerta');
      }

      for (let i = 1; i <= 40; i++) {
        await new Promise((s) => setTimeout(s, 1000));
        const at = await api.get('/v1/pix/pagamentos/' + e2e);
        const pp = at.corpo;
        trocar('#spi-status',
          kpi('status', pp.status, pp.status === 'RESERVED' ? 'aviso' : 'ok'),
          kpi('aguardando', i + 's', 'aviso'),
          kpi('http', r.status));
        if (pp.status !== 'RESERVED') {
          empurrar('t+' + (Math.round(r.ms / 1000) + i) + 's', 'Reconciliação concluiu: ' + pp.status,
            'a reconciliação perguntou ao SPI pelo EndToEndId e recuperou o desfecho.', 'ok');
          if (spiInfo.ok) {
            const janela = new Date(pp.atualizado_em) - new Date(spiInfo.corpo.liquidado_em);
            $('#spi-janela').innerHTML =
              `<b>${(janela / 1000).toFixed(1)}s</b> de <strong>janela de incerteza</strong>: o dinheiro já era
               irrevogável no SPB, e o TechPix ainda não sabia. Não estava errado — estava <em>incerto</em>,
               e sabia disso. O tamanho dessa janela é decisão de negócio
               (<code>SPI_TIMEOUT_MS</code> + <code>RECONCILER_AFTER_S</code>), não acidente técnico.`;
          }
          break;
        }
      }
    }
    btn.disabled = false;
  },
};

/* ===========================================================================
   CONTENÇÃO
   =========================================================================== */
const contencao = {
  montar() {
    $('#btn-contencao').addEventListener('click', contencao.disparar);
    $('#btn-ct-preparar').addEventListener('click', contencao.preparar);
    ['#ct-n', '#ct-valor'].forEach((s) => $(s).addEventListener('input', contencao.previsao));
    contencao.previsao();
  },
  atualizar() { encherSelect($('#ct-conta'), CONTAS, 'carteira:carla'); contencao.previsao(); },

  async previsao() {
    const conta = $('#ct-conta').value;
    if (!conta) return;
    const r = await api.get(`/v1/contas/${conta}/saldo`);
    const saldo = r.corpo?.forte?.saldo_centavos ?? 0;
    const valor = Math.round(parseFloat($('#ct-valor').value || '0') * 100);
    const cabem = valor > 0 ? Math.floor(saldo / valor) : 0;
    $('#ct-previsao').textContent = `saldo ${brl(saldo)} — cabem ${cabem} de ${brl(valor)}`;
  },

  async preparar() {
    const conta = $('#ct-conta').value;
    const valor = Math.round(parseFloat($('#ct-valor').value || '0') * 100);
    const saldo = (await api.get(`/v1/contas/${conta}/saldo`)).corpo?.forte?.saldo_centavos ?? 0;
    const alvo = valor * 6;
    if (saldo >= alvo) return brinde('saldo já suficiente', 'ok');
    await api.post(`/v1/contas/${conta}/depositos`, { valor_centavos: alvo - saldo, descricao: 'preparo da demo' },
      { 'Idempotency-Key': 'ct-' + Date.now() });
    brinde(`conta preparada com ${brl(alvo)}`, 'ok');
    contencao.previsao();
  },

  async disparar() {
    const btn = $('#btn-contencao'); btn.disabled = true;
    const conta = $('#ct-conta').value;
    const n = +$('#ct-n').value;
    const centavos = Math.round(parseFloat($('#ct-valor').value || '0') * 100);

    const antes = await api.get('/v1/latencia');
    const saldoAntes = (await api.get(`/v1/contas/${conta}/saldo`)).corpo?.forte?.saldo_centavos ?? 0;
    const cabem = Math.floor(saldoAntes / centavos);

    trocar('#ct-grade', Array.from({ length: n }, (_, i) =>
      el('div', { class: 'tiro rodando', id: 'tiro-' + i },
        el('div', { class: 'tiro-num' }, '#' + (i + 1)),
        el('div', { class: 'tiro-http' }, '…'),
        el('div', { class: 'tiro-motivo' }, 'enviando'))));

    const chaves = await Promise.all(Array.from({ length: n }, () => novoE2E()));
    const resultados = await Promise.all(chaves.map(async (e2e, i) => {
      const r = await api.post('/v1/pix/pagamentos', {
        e2e_id: e2e, conta_pagador: conta, chave_recebedor: 'bruno@bancobeta.com.br',
        valor_centavos: centavos, descricao: 'teste de contenção',
      });
      const ok = r.ok;
      trocar('#tiro-' + i,
        el('div', { class: 'tiro-num' }, '#' + (i + 1)),
        el('div', { class: 'tiro-http' }, r.status),
        el('div', { class: 'tiro-motivo' }, ok ? (r.corpo?.pagamento?.status || 'ok') : (r.corpo?.erro?.codigo || 'erro')));
      $('#tiro-' + i).className = 'tiro ' + (ok ? 'aprovado' : 'recusado');
      return r;
    }));

    const depois = await api.get('/v1/latencia');
    const saldoDepois = (await api.get(`/v1/contas/${conta}/saldo`)).corpo?.forte?.saldo_centavos ?? 0;
    const aprovados = resultados.filter((r) => r.ok).length;
    const cont = (o, k) => o.corpo?.contadores?.[k] ?? 0;
    const retries = cont(depois, 'ledger.serialization_retry') - cont(antes, 'ledger.serialization_retry');

    trocar('#ct-kpis',
      kpi('aprovados', aprovados, aprovados === Math.min(cabem, n) ? 'ok' : 'perigo'),
      kpi('recusados', n - aprovados),
      kpi('cabiam', Math.min(cabem, n), 'forte'),
      kpi('retries de serialização', retries, retries ? 'aviso' : ''),
      kpi('saldo final', brl(saldoDepois), saldoDepois >= 0 ? 'ok' : 'perigo'),
      kpi('estourou?', saldoDepois >= 0 ? 'NÃO' : 'SIM', saldoDepois >= 0 ? 'ok' : 'perigo'));

    if (aprovados !== Math.min(cabem, n)) {
      brinde(`atenção: passaram ${aprovados}, cabiam ${Math.min(cabem, n)}`, 'erro');
    }
    btn.disabled = false;
  },
};

/* ===========================================================================
   ORÇAMENTO DE LATÊNCIA
   =========================================================================== */
const CORES_PASSO = ['#4da3ff', '#b98cff', '#2ee6a8', '#ffc44d', '#ff9f6b', '#ff6b6b'];

const latencia = {
  intervalo: 3000,
  async atualizar() {
    if (ULTIMO_ORCAMENTO) {
      const teto = ULTIMO_ORCAMENTO.teto_normativo_ms;
      const passos = ULTIMO_ORCAMENTO.passos;
      const usado = ULTIMO_ORCAMENTO.total_ms;
      // Escala relativa ao consumo, senão 400ms em 40s viram um fio invisível.
      const escala = Math.max(usado * 1.35, teto * 0.02);

      trocar('#orcamento-barra',
        ...passos.map((p, i) => el('div', {
          class: 'orc-passo',
          style: `width:${(p.ms / escala) * 100}%; background:${CORES_PASSO[i % CORES_PASSO.length]}`,
          title: `${p.nome}: ${ms(p.ms)}`,
        }, p.ms / escala > 0.12 ? ms(p.ms) : '')),
        el('div', { class: 'orc-folga' }, `folga até o teto de 40s — usado ${ULTIMO_ORCAMENTO.consumo_do_teto}`));

      trocar('#orcamento-legenda', passos.map((p, i) =>
        el('span', {}, el('i', { style: `background:${CORES_PASSO[i % CORES_PASSO.length]}` }),
          p.nome.replace(/^\d_/, '').replace(/_/g, ' ') + ' · ' + ms(p.ms))));
    } else {
      trocar('#orcamento-barra', el('div', { class: 'orc-folga' }, 'execute um pagamento para ver o orçamento'));
    }

    const r = await api.get('/v1/latencia');
    if (!r.ok) return;
    const q = r.corpo.quantis_por_passo || {};
    const nomes = Object.keys(q).sort();
    const maxP99 = Math.max(1, ...nomes.map((n) => q[n].p99_ms));

    trocar('#tabela-quantis', tabela(['passo', 'amostras', 'p50', 'p99', 'p99.9', ''],
      nomes.map((n) => ({
        celulas: [
          el('td', {}, n.replace(/^\d_/, '').replace(/_/g, ' ')),
          el('td', { class: 'num' }, q[n].amostras),
          el('td', { class: 'num' }, ms(q[n].p50_ms)),
          el('td', { class: 'num' }, ms(q[n].p99_ms)),
          el('td', { class: 'num' }, ms(q[n].p999_ms)),
          el('td', {}, el('div', { class: 'barra-mini' },
            el('div', { style: `width:${(q[n].p99_ms / maxP99) * 100}%` }))),
        ],
      }))));

    const ref = r.corpo.referencias;
    trocar('#tabela-referencias', tabela(['referência', 'valor'], [
      { celulas: [el('td', {}, 'Teto normativo do Pix (Res. BCB 195/2022)'), el('td', { class: 'num' }, ref.teto_normativo_pix_ms + 'ms')] },
      { celulas: [el('td', {}, 'SPI real — p50'), el('td', { class: 'num' }, ref.spi_real_p50_ms + 'ms')] },
      { celulas: [el('td', {}, 'SPI real — p99'), el('td', { class: 'num' }, ref.spi_real_p99_ms + 'ms')] },
      { celulas: [el('td', {}, 'DICT — SLA de consulta (p99)'), el('td', { class: 'num' }, ref.dict_sla_p99_ms + 'ms')] },
    ]));

    const c = r.corpo.contadores || {};
    trocar('#tabela-contadores', tabela(['evento', 'total'],
      Object.keys(c).sort().map((k) => ({
        celulas: [el('td', {}, el('code', {}, k)), el('td', { class: 'num' }, c[k])],
      }))));
  },
};

/* ===========================================================================
   HARNESS
   =========================================================================== */
const harness = {
  intervalo: 4000,
  montar() {
    $$('[data-tentativa]').forEach((b) => b.addEventListener('click', async () => {
      b.disabled = true;
      const r = await api.post('/v1/ledger/tentativas', { tipo: b.dataset.tentativa, conta: 'carteira:ana' });
      b.disabled = false;
      const t = r.corpo;
      $('#tentativas-log').prepend(el('div', { class: 'log-linha ' + (t.bloqueado ? 'ok' : 'perigo') },
        el('span', { class: 'log-hora' }, hora()),
        el('span', { class: 'log-msg' },
          el('div', {}, el('b', {}, t.bloqueado ? '🛡 BLOQUEADO' : '💥 PASSOU'), ' · ' + t.explicacao),
          el('div', { class: 'mini' }, 'camada: ' + t.camada),
          el('div', { class: 'mini' }, t.erro))));
      harness.atualizar();
      if (!t.bloqueado) brinde('um guardrail falhou — isso é incidente', 'erro');
    }));
  },

  async atualizar() {
    const r = await api.get('/v1/fitness');
    if (!r.corpo) return;
    const d = r.corpo;
    const selo = $('#fitness-selo');
    selo.className = 'selo ' + (d.aprovado ? 'ok' : 'falhou');
    selo.textContent = d.aprovado ? 'todas as invariantes de pé' : 'INVARIANTE VIOLADA';

    trocar('#fitness-completo', (d.checks || []).map((c) => el('div', { class: 'check-item' },
      el('div', { class: 'check-selo ' + (c.aprovado ? 'ok' : 'falhou') }, c.aprovado ? '✓' : '✕'),
      el('div', {},
        el('div', { class: 'check-nome' }, c.nome.replace(/_/g, ' ')),
        el('div', { class: 'check-inv' }, c.invariante),
        el('div', { class: 'check-det' }, c.detalhe)))),
      el('div', { class: 'kpis' },
        kpi('transações', d.resumo?.transacoes ?? '—'),
        kpi('lançamentos', d.resumo?.lancamentos ?? '—'),
        kpi('por transação', (d.resumo?.lancamentos_por_transacao ?? 0).toFixed(1)),
        kpi('modo de lock', d.resumo?.modo_de_lock ?? '—', 'pequeno')));
  },
};

/* ===========================================================================
   BOOT
   =========================================================================== */
const SECOES = { visao, toques, ledger, consistencia, dict, spi, contencao, latencia, harness };

(async function boot() {
  const info = await api.get('/v1/info');
  if (info.ok) {
    BACEN = info.corpo.bacen_publico || BACEN;
    $('#status-api').textContent = 'API no ar';
    $('#status-api').className = 'pill ok';
    const c = info.corpo.config_visivel || {};
    $('#config-lock').textContent =
      `lock ${c.modo_lock_ledger} · pool ${c.pool_conexoes} · projeção +${c.atraso_projecao_ms}ms · SPI timeout ${c.spi_timeout_ms}ms`;
  } else {
    $('#status-api').textContent = 'API fora do ar';
    $('#status-api').className = 'pill ruim';
  }

  const b = await bacen.get('/healthz');
  $('#status-bacen').textContent = b.ok ? 'BACEN simulado no ar' : 'BACEN inacessível';
  $('#status-bacen').className = 'pill ' + (b.ok ? 'ok' : 'ruim');

  await visao.atualizar();   // popula selects antes de montar as outras seções
  irPara(location.hash.slice(1) || 'visao');
})();
